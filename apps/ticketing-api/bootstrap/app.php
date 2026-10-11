<?php

use App\Exceptions\BusinessRuleException;
use App\Exceptions\InjectedFault;
use App\Http\Middleware\InstanceHeaders;
use Illuminate\Database\Eloquent\ModelNotFoundException;
use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;
use Illuminate\Http\Request;
use Illuminate\Routing\Middleware\SubstituteBindings;
use Illuminate\Validation\ValidationException;
use Symfony\Component\HttpKernel\Exception\NotFoundHttpException;

return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__.'/../routes/web.php',
        api: __DIR__.'/../routes/api.php',
        commands: __DIR__.'/../routes/console.php',
    )
    ->withMiddleware(function (Middleware $middleware): void {
        // Stateless JSON service: no sessions, cookies or CSRF on any route.
        $middleware->group('web', [SubstituteBindings::class]);
        $middleware->append(InstanceHeaders::class);
    })
    ->withExceptions(function (Exceptions $exceptions): void {
        $exceptions->shouldRenderJsonWhen(fn (Request $request) => true);
        $exceptions->render(function (NotFoundHttpException $e) {
            $message = $e->getPrevious() instanceof ModelNotFoundException ? 'Resource not found.' : 'Route not found.';

            return response()->json(['error' => 'not_found', 'message' => $message], 404);
        });
        $exceptions->render(fn (ValidationException $e) => response()->json([
            'error' => 'validation_failed',
            'message' => $e->getMessage(),
            'details' => $e->errors(),
        ], 422));
        $exceptions->render(fn (InjectedFault $e) => response()->json([
            'error' => 'injected_fault',
            'message' => $e->getMessage(),
        ], 500));
        $exceptions->dontReport([BusinessRuleException::class]);
    })->create();
