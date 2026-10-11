<?php

namespace App\Exceptions;

use RuntimeException;

/** Thrown deliberately by FaultInjector to simulate a crash before a transaction commits. */
class InjectedFault extends RuntimeException {}
