# syntax=docker/dockerfile:1.7
FROM composer:2 AS vendor
WORKDIR /app
COPY composer.json composer.lock ./
RUN composer install --no-dev --no-scripts --no-autoloader --prefer-dist --no-interaction --ignore-platform-reqs
COPY . .
RUN composer dump-autoload --optimize --classmap-authoritative --no-dev --no-scripts

FROM php:8.4-fpm-alpine
RUN apk add --no-cache nginx supervisor curl jq libpq gettext \
    && apk add --no-cache --virtual .build-deps $PHPIZE_DEPS postgresql-dev linux-headers \
    && docker-php-ext-install -j"$(nproc)" pdo_pgsql pcntl opcache \
    && apk del .build-deps \
    && rm -f /usr/local/etc/php-fpm.d/www.conf /usr/local/etc/php-fpm.d/zz-docker.conf /usr/local/etc/php-fpm.d/docker.conf /etc/nginx/http.d/default.conf \
    && mkdir -p /run/nginx

WORKDIR /var/www/html
COPY --from=vendor --chown=www-data:www-data /app /var/www/html
COPY docker/php.ini /usr/local/etc/php/conf.d/zz-lab.ini
COPY docker/php-fpm-www.conf.template /usr/local/etc/php-fpm.d/www.conf.template
COPY docker/nginx-main.conf /etc/nginx/nginx.conf
COPY docker/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY docker/supervisord.conf /etc/supervisord.conf
COPY --chmod=0755 docker/entrypoint.sh /usr/local/bin/lab-entrypoint
COPY --chmod=0755 docker/cpu-sampler.sh /usr/local/bin/cpu-sampler

ENV APP_ENV=production APP_DEBUG=false LOG_CHANNEL=stderr LOG_LEVEL=warning \
    DB_CONNECTION=pgsql CACHE_STORE=file SESSION_DRIVER=array QUEUE_CONNECTION=sync
RUN php artisan package:discover --ansi && php artisan route:cache \
    && chown -R www-data:www-data storage bootstrap/cache \
    # A preload that fails would stop PHP-FPM from starting: exercise it here so the build fails instead.
    && php -d opcache.enable_cli=1 -r 'echo "preloaded classes: ", count(opcache_get_status()["preload_statistics"]["classes"] ?? []), "\n";'

ARG APP_VERSION=dev
ENV APP_VERSION=${APP_VERSION}
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lab-entrypoint"]
