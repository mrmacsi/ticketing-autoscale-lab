<?php

namespace App\Providers;

use App\Services\FaultInjector;
use Illuminate\Support\ServiceProvider;

class AppServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        // Armed by middleware per request, tripped by services inside transactions.
        $this->app->scoped(FaultInjector::class);
    }

    public function boot(): void {}
}
