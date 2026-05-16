# syntax=docker/dockerfile:1.6

FROM php:8.3-fpm-bookworm

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        nginx \
        unzip \
        libssl-dev \
        pkg-config \
        curl \
        ca-certificates \
        git \
 && pecl install mongodb \
 && docker-php-ext-enable mongodb \
 && curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer \
 && rm -rf /var/lib/apt/lists/*

# Tune php-fpm to keep enough workers ready for the validator's parallel load
# (22 t.Parallel scenarios + sub-requests). Daemonize off so docker can manage it.
RUN { \
        echo '[global]'; \
        echo 'daemonize = no'; \
        echo '[www]'; \
        echo 'listen = 127.0.0.1:9000'; \
        echo 'pm = static'; \
        echo 'pm.max_children = 32'; \
        echo 'pm.max_requests = 1000'; \
        echo 'access.log = /proc/self/fd/2'; \
        echo 'catch_workers_output = yes'; \
        echo 'decorate_workers_output = no'; \
        echo 'clear_env = no'; \
    } > /usr/local/etc/php-fpm.d/zz-overrides.conf

# Allow larger payloads.
RUN { \
        echo 'memory_limit = 256M'; \
        echo 'post_max_size = 32M'; \
        echo 'upload_max_filesize = 32M'; \
        echo 'max_execution_time = 60'; \
        echo 'opcache.enable=1'; \
        echo 'opcache.enable_cli=0'; \
        echo 'opcache.validate_timestamps=0'; \
        echo 'opcache.max_accelerated_files=10000'; \
    } > /usr/local/etc/php/conf.d/zz-overrides.ini

WORKDIR /app

COPY composer.json composer.lock* ./
RUN composer install --no-dev --no-progress --no-interaction --no-autoloader

COPY public ./public
COPY src    ./src
COPY nginx.conf /etc/nginx/sites-available/default

# Build the autoloader AFTER src/ is in place so the class map sees our code.
RUN composer dump-autoload --optimize --classmap-authoritative --no-dev

# nginx.conf must be enabled and the default removed.
RUN rm -f /etc/nginx/sites-enabled/default \
 && ln -sf /etc/nginx/sites-available/default /etc/nginx/sites-enabled/default

EXPOSE 8080

CMD ["sh", "-c", "php-fpm -D && nginx -g 'daemon off;'"]
