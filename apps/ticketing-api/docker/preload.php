<?php

/**
 * OPcache preload (php.ini `opcache.preload`), run once by the PHP-FPM master at start: the classes a
 * typical API request uses are compiled and linked into shared memory, so no worker has to declare and
 * link them on every request (measured: about 15 % less CPU per request on a single core).
 *
 * The class list comes from a traced request mix (docker/preload-trace.php). Loading goes through the
 * Composer autoloader so parents and interfaces are linked in the right order; a name that no longer
 * exists after a dependency update is simply skipped. Code changes need a restart to be picked up,
 * which opcache.validate_timestamps=0 already implies.
 */
require __DIR__.'/../vendor/autoload.php';

foreach (file(__DIR__.'/preload-classes.txt', FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) as $name) {
    if ($name[0] !== '#') {
        class_exists($name) || interface_exists($name) || trait_exists($name) || enum_exists($name);
    }
}
