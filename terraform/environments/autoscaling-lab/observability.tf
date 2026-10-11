# Written in the form the Monitoring API returns (targetAxis set, zero xPos/yPos omitted); anything
# else shows up as an in-place change on every plan.
resource "google_monitoring_dashboard" "lab" {
  count = var.enable_workloads ? 1 : 0
  dashboard_json = jsonencode({
    displayName = "Ticketing autoscaling lab"
    mosaicLayout = {
      columns = 12
      tiles = [
        { width = 6, height = 4, widget = { title = "Application VM CPU utilisation", xyChart = { dataSets = [{
          plotType   = "LINE"
          targetAxis = "Y1"
          timeSeriesQuery = { timeSeriesFilter = {
            filter      = "metric.type=\"compute.googleapis.com/instance/cpu/utilization\" AND metadata.user_labels.\"lab-role\"=\"app\""
            aggregation = { alignmentPeriod = "60s", perSeriesAligner = "ALIGN_MEAN" }
          } }
        }], thresholds = [{ value = var.target_cpu_utilization, label = "autoscaler target" }] } } },
        { xPos = 6, width = 6, height = 4, widget = { title = "Load balancer requests/s by response class", xyChart = { dataSets = [{
          plotType   = "STACKED_AREA"
          targetAxis = "Y1"
          timeSeriesQuery = { timeSeriesFilter = {
            filter      = "metric.type=\"loadbalancing.googleapis.com/https/request_count\" AND resource.label.\"forwarding_rule_name\"=\"${var.prefix}-http\""
            aggregation = { alignmentPeriod = "60s", perSeriesAligner = "ALIGN_RATE", crossSeriesReducer = "REDUCE_SUM", groupByFields = ["metric.label.\"response_code_class\""] }
          } }
        }] } } },
        { yPos = 4, width = 6, height = 4, widget = { title = "Load balancer latency p95 (ms)", xyChart = { dataSets = [{
          plotType   = "LINE"
          targetAxis = "Y1"
          timeSeriesQuery = { timeSeriesFilter = {
            filter      = "metric.type=\"loadbalancing.googleapis.com/https/total_latencies\" AND resource.label.\"forwarding_rule_name\"=\"${var.prefix}-http\""
            aggregation = { alignmentPeriod = "60s", perSeriesAligner = "ALIGN_PERCENTILE_95", crossSeriesReducer = "REDUCE_MEAN" }
          } }
        }] } } },
        { xPos = 6, yPos = 4, width = 6, height = 4, widget = { title = "Cloud SQL CPU utilisation", xyChart = { dataSets = [{
          plotType   = "LINE"
          targetAxis = "Y1"
          timeSeriesQuery = { timeSeriesFilter = {
            filter      = "metric.type=\"cloudsql.googleapis.com/database/cpu/utilization\" AND resource.label.\"database_id\"=\"${var.project_id}:${module.database.instance_name}\""
            aggregation = { alignmentPeriod = "60s", perSeriesAligner = "ALIGN_MEAN" }
          } }
        }] } } },
      ]
    }
  })
}

# Notifies billing-account admins at 50/90/100 % of the monthly amount. A budget is an alert,
# NOT a spending cap: resources keep running (and billing) until destroyed.
resource "google_billing_budget" "lab" {
  count           = var.billing_account_id != "" && var.budget_amount > 0 ? 1 : 0
  billing_account = var.billing_account_id
  display_name    = "${var.prefix} monthly alert"

  budget_filter {
    projects = ["projects/${var.project_number}"]
    labels   = { lab = var.prefix }
  }

  amount {
    specified_amount {
      units = tostring(var.budget_amount)
    }
  }

  threshold_rules {
    threshold_percent = 0.5
  }
  threshold_rules {
    threshold_percent = 0.9
  }
  threshold_rules {
    threshold_percent = 1.0
  }

  depends_on = [google_project_service.apis]
}
