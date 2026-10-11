"""Offline teardown regressions: no credentials, network calls or cloud mutations."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("destroy_state", ROOT / "scripts/destroy-state.py")
destroy_state = importlib.util.module_from_spec(spec)
spec.loader.exec_module(destroy_state)
retry_spec = importlib.util.spec_from_file_location("transient_error", ROOT / "scripts/transient-terraform-error.py")
transient_error = importlib.util.module_from_spec(retry_spec)
retry_spec.loader.exec_module(transient_error)
PROJECT = "contentpulse-vertex-ai"
PREFIX = "ticketing-scale-lab"


def sql_state():
    def resource(kind, name, attributes):
        return {"module": "module.database", "mode": "managed", "type": kind,
                "name": name, "instances": [{"attributes": attributes}]}
    parent = {"project": PROJECT, "name": f"{PREFIX}-pg", "deletion_policy": "DELETE",
              "deletion_protection": False, "settings": [{"deletion_protection_enabled": False}]}
    children = [("google_sql_database", "ticketing", "ticketing"),
                ("google_sql_user", "app", "ticketing_app"),
                ("google_sql_user", "observer", "lab_observer")]
    return {"resources": [resource("google_sql_database_instance", "main", parent)] + [
        resource(kind, name, {"project": PROJECT, "instance": parent["name"],
                              "name": sql_name, "deletion_policy": "DELETE"})
        for kind, name, sql_name in children]}


class StateSafetyTests(unittest.TestCase):
    def test_legacy_children_can_be_skipped_but_parent_stays_managed(self):
        state = sql_state()
        original = copy.deepcopy(state)
        addresses = destroy_state.sql_children_to_forget(state, PROJECT, PREFIX)
        self.assertEqual(len(addresses), 3)
        self.assertNotIn("module.database.google_sql_database_instance.main", addresses)
        self.assertEqual(state, original)

    def test_foreign_instance_is_rejected(self):
        for key, value in [("project", "another-project"), ("name", "production-pg")]:
            with self.subTest(key=key):
                state = sql_state()
                state["resources"][0]["instances"][0]["attributes"][key] = value
                with self.assertRaises(ValueError):
                    destroy_state.sql_children_to_forget(state, PROJECT, PREFIX)

    def test_protection_is_not_bypassed(self):
        for key, value in [("deletion_protection", True), ("deletion_policy", "ABANDON")]:
            with self.subTest(key=key):
                state = sql_state()
                state["resources"][0]["instances"][0]["attributes"][key] = value
                with self.assertRaises(ValueError):
                    destroy_state.sql_children_to_forget(state, PROJECT, PREFIX)
        state = sql_state()
        state["resources"][0]["instances"][0]["attributes"]["settings"][0]["deletion_protection_enabled"] = True
        with self.assertRaises(ValueError):
            destroy_state.sql_children_to_forget(state, PROJECT, PREFIX)

    def test_missing_parent_or_foreign_child_is_rejected(self):
        state = sql_state()
        state["resources"].pop(0)
        with self.assertRaises(ValueError):
            destroy_state.sql_children_to_forget(state, PROJECT, PREFIX)
        state = sql_state()
        state["resources"][1]["instances"][0]["attributes"]["instance"] = "production-pg"
        with self.assertRaises(ValueError):
            destroy_state.sql_children_to_forget(state, PROJECT, PREFIX)

    def test_empty_state_is_already_clean(self):
        self.assertEqual(destroy_state.sql_children_to_forget({}, PROJECT, PREFIX), [])


class TransportClassificationTests(unittest.TestCase):
    def test_pasted_provider_eof_error_is_retryable(self):
        # Use a representative provider diagnostic; tests do not depend on user attachments.
        self.assertTrue(transient_error.is_transient_terraform_failure('│ Error: Error reading Service Account: Get "https://iam.googleapis.com/": EOF\n│\n│ with google_service_account.loadgen,\n╵'))

    def test_mixed_permanent_and_transient_errors_are_not_retryable(self):
        self.assertFalse(transient_error.is_transient_terraform_failure("Error: read: EOF\n\nError: Error 403: permission denied\n"))

    def test_warning_eof_and_unknown_failures_are_not_retryable(self):
        for output in ("Warning: EOF\nError: invalid configuration\n", "Error: State lock timeout\n", "read: EOF"):
            with self.subTest(output=output):
                self.assertFalse(transient_error.is_transient_terraform_failure(output))

    def test_lost_http2_connection_is_retryable(self):
        self.assertTrue(transient_error.is_transient_terraform_failure("Error: Error polling deletion: http2: client connection lost\n"))

    def test_wrapped_transport_error_is_retryable(self):
        self.assertTrue(transient_error.is_transient_terraform_failure("│ Error: Error reading Service Account\n│ Get https://iam.googleapis.com/: connection reset by peer\n│\n"))

    def test_unreachable_ipv6_route_is_retryable_during_planning(self):
        self.assertTrue(transient_error.is_transient_terraform_failure("Error: Get https://compute.googleapis.com/: dial tcp [2a00:1450::5f]:443: connect: no route to host\n"))


class CleanupFlowTests(unittest.TestCase):
    def run_shell(self, body, *, api_error=False, stage_error=False, unsafe_plan=False, plan_failures=0, plan_error="EOF", final_unsafe=False, apply_failures=0, apply_error="EOF"):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            (folder / "state.json").write_text(json.dumps(sql_state()))
            stage_plan = {"resource_changes": [{
                "address": "module.networking.google_compute_subnetwork.control" if unsafe_plan
                else "module.database.google_sql_database_instance.main",
                "change": {"actions": ["delete"]}}]}
            (folder / "stage-plan.json").write_text(json.dumps(stage_plan))
            final_plan = {"resource_changes": [{"address": "module.networking.google_compute_network.lab",
                           "change": {"actions": ["create"] if final_unsafe else ["delete"]}}]}
            (folder / "final-plan.json").write_text(json.dumps(final_plan))
            prelude = r'''
source "$TEST_ROOT/scripts/lib.sh"
source "$TEST_ROOT/scripts/cleanup-lib.sh"
TF_DIR="$TEST_TMP"
TEARDOWN_VPC_RELEASE_TIMEOUT_SECONDS=2
TEARDOWN_POLL_INTERVAL_SECONDS=1
terraform() {
    printf 'terraform %s\n' "$*" >> "$TEST_TMP/calls"
    case "$2 $3" in
        'state pull') cat "$TEST_TMP/state.json" ;;
        'state list') printf '%s\n' module.database.google_sql_database_instance.main 'module.control_plane[0].google_cloud_run_v2_service.control' 'module.application[0].google_compute_instance_group_manager.app' 'module.load_generator[0].google_compute_instance.loadgen' 'module.networking.google_compute_subnetwork.control' ;;
        'show -json')
            if [ "$4" = destroy.tfplan ]; then cat "$TEST_TMP/final-plan.json";
            else cat "$TEST_TMP/stage-plan.json"; fi ;;
    esac
}
tf() {
    printf 'tf %s\n' "$*" >> "$TEST_TMP/calls"
    if [ "$TEST_STAGE_ERROR" = 1 ] && [ "$1" = apply ]; then return 1; fi
    if [ "$1" = apply ]; then
        local applied=0
        if [ -f "$TEST_TMP/apply-attempts" ]; then applied="$(cat "$TEST_TMP/apply-attempts")"; fi
        applied=$((applied + 1))
        echo "$applied" > "$TEST_TMP/apply-attempts"
        if [ "$applied" -le "$TEST_APPLY_FAILURES" ]; then
            echo "Error: Error polling deletion operation: $TEST_APPLY_ERROR" >&2
            return 1
        fi
    fi
    if [ "$1" = plan ]; then
        local count=0
        if [ -f "$TEST_TMP/attempts" ]; then count="$(cat "$TEST_TMP/attempts")"; fi
        count=$((count + 1))
        echo "$count" > "$TEST_TMP/attempts"
        if [ "$count" -le "$TEST_PLAN_FAILURES" ]; then
            echo "Error: Error reading service account: $TEST_PLAN_ERROR" >&2
            return 1
        fi
    fi
}
gcloud_safe() {
    printf 'gcloud %s\n' "$*" >> "$TEST_TMP/calls"
    if [ "$TEST_API_ERROR" = 1 ]; then echo 'API permission denied' >&2; return 1; fi
    if [ ! -f "$TEST_TMP/released" ]; then echo serverless-ipv4-cloudrun-test; fi
}
sleep() {
    printf 'sleep %s\n' "$*" >> "$TEST_TMP/calls"
    if [ "${TEST_NEVER_RELEASE:-0}" != 1 ]; then touch "$TEST_TMP/released"; fi
    SECONDS=$((SECONDS + $1))
}
'''
            environment = {**os.environ, "TEST_ROOT": str(ROOT), "TEST_TMP": directory,
                           "TEST_API_ERROR": str(int(api_error)),
                           "TEST_STAGE_ERROR": str(int(stage_error)), "TEST_PLAN_FAILURES": str(plan_failures), "TEST_PLAN_ERROR": plan_error, "TEST_APPLY_FAILURES": str(apply_failures), "TEST_APPLY_ERROR": apply_error}
            result = subprocess.run(["bash", "-c", prelude + body], env=environment,
                                    capture_output=True, text=True, timeout=10)
            calls = (folder / "calls").read_text() if (folder / "calls").exists() else ""
            return result, calls

    def test_delete_sql_and_workloads_before_waiting_then_delete_network(self):
        result, calls = self.run_shell("destroy_lab_infrastructure")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertLess(calls.index("state rm"), calls.index("tf plan"))
        self.assertLess(calls.index("workloads-destroy.tfplan\ngcloud"), calls.index("sleep 1"))
        self.assertLess(calls.index("sleep 1"), calls.index("tf apply -input=false -lock-timeout=60s -parallelism=2 destroy.tfplan"))
        plan = next(line for line in calls.splitlines() if line.startswith("tf plan"))
        self.assertIn("-target=module.application[0]", plan)
        self.assertIn("-target=module.control_plane[0]", plan)
        self.assertIn("-target=module.load_generator[0]", plan)
        self.assertNotIn("-target=module.networking", plan)

    def test_api_failure_stops_before_final_destroy(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", api_error=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("API permission denied", result.stderr)
        self.assertNotIn("tf apply -input=false -lock-timeout=60s -parallelism=2 destroy.tfplan", calls)

    def test_failed_sql_or_workload_deletion_stops_before_network(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", stage_error=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("gcloud", calls)
        self.assertEqual(sum(line.startswith("tf apply") for line in calls.splitlines()), 1)
        self.assertNotIn("tf apply -input=false -lock-timeout=60s -parallelism=2 destroy.tfplan", calls)

    def test_target_plan_cannot_delete_network_early(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", unsafe_plan=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Unsafe workload destroy plan", result.stderr)
        self.assertNotIn("tf apply", calls)

    def test_ip_release_timeout_leaves_network_pending(self):
        result, calls = self.run_shell("TEST_NEVER_RELEASE=1\ndestroy_lab_infrastructure")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Timed out waiting", result.stderr)
        self.assertNotIn("tf apply -input=false -lock-timeout=60s -parallelism=2 destroy.tfplan", calls)

    def test_transient_plan_failure_retries_before_any_apply(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", plan_failures=1)
        self.assertEqual(result.returncode, 0, result.stderr)
        plans = [line for line in calls.splitlines() if line.startswith("tf plan")]
        self.assertEqual(len(plans), 3)  # two workload plans, one final plan
        self.assertEqual(plans[0], plans[1])
        self.assertLess(calls.index("sleep 5"), calls.index("tf apply"))
        self.assertIn("attempt 2/3", result.stderr)

    def test_repeated_transient_failures_exhaust_retry_limit(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", plan_failures=4)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum(line.startswith("tf plan") for line in calls.splitlines()), 3)
        self.assertNotIn("tf apply", calls)
        self.assertNotIn("gcloud", calls)

    def test_permanent_plan_failure_is_not_retried(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", plan_failures=1, plan_error="Error 403: permission denied")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum(line.startswith("tf plan") for line in calls.splitlines()), 1)
        self.assertNotIn("sleep", calls)
        self.assertNotIn("tf apply", calls)

    def test_final_plan_also_retries_and_apply_runs_once(self):
        result, calls = self.run_shell("destroy_plan_with_retry destroy.tfplan\ntf apply destroy.tfplan", plan_failures=1)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(line.startswith("tf plan") for line in calls.splitlines()), 2)
        self.assertEqual(sum(line.startswith("tf apply") for line in calls.splitlines()), 1)

    def test_failed_deletion_poll_rebuilds_plan_before_applying_again(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", apply_failures=1,
                                      apply_error="dial tcp [2a00:1450::5f]:443: connect: no route to host")
        self.assertEqual(result.returncode, 0, result.stderr)
        operations = [line for line in calls.splitlines() if line.startswith(("tf plan", "tf apply"))]
        self.assertEqual([line.split()[1] for line in operations], ["plan", "apply", "plan", "apply", "plan", "apply"])
        self.assertEqual(operations[0], operations[2])
        self.assertIn("rebuilding the destroy plan", result.stderr)

    def test_permanent_apply_failure_is_not_retried(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", apply_failures=1, apply_error="Error 403: permission denied")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum(line.startswith("tf apply") for line in calls.splitlines()), 1)
        self.assertNotIn("sleep", calls)
        self.assertNotIn("gcloud", calls)

    def test_repeated_apply_transport_failures_stop_at_three_new_plans(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", apply_failures=4)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sum(line.startswith("tf plan") for line in calls.splitlines()), 3)
        self.assertEqual(sum(line.startswith("tf apply") for line in calls.splitlines()), 3)
        self.assertNotIn("gcloud", calls)

    def test_cleanup_transport_setting_is_scoped_to_terraform(self):
        result, calls = self.run_shell('GODEBUG=netdns=go\ntf() { echo "$GODEBUG"; }\ntf_for_cleanup plan\n[ "$GODEBUG" = netdns=go ]')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("netdns=go,http2client=0", result.stdout)

    def test_final_plan_cannot_create_resources(self):
        result, calls = self.run_shell("destroy_lab_infrastructure", final_unsafe=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Unsafe final destroy plan", result.stderr)
        self.assertNotIn("tf apply -input=false -lock-timeout=60s -parallelism=2 destroy.tfplan", calls)

    def test_stale_saved_plan_is_removed_after_planning_failure(self):
        result, calls = self.run_shell('touch "$TF_DIR/destroy.tfplan"\nset +e\ndestroy_plan_with_retry destroy.tfplan\nstatus=$?\n[ "$status" -ne 0 ] && [ ! -f "$TF_DIR/destroy.tfplan" ]',
                                      plan_failures=1, plan_error="permission denied")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("tf apply", calls)

    def test_failed_verification_is_recorded_as_a_leftover(self):
        result, _ = self.run_shell('leftovers=""\ncheck_leftovers "Cloud SQL" gcloud_safe sql instances list\n[ -n "$leftovers" ]\n', api_error=True)
        self.assertEqual(result.returncode, 0)
        self.assertNotIn("none", result.stdout)
        self.assertIn("Could not verify Cloud SQL", result.stderr)


if __name__ == "__main__":
    unittest.main()
