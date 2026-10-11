package dev.sentinel.ingestion;

import org.springframework.boot.CommandLineRunner;
import org.springframework.boot.SpringApplication;
import org.springframework.context.ApplicationContext;
import org.springframework.context.annotation.Profile;
import org.springframework.stereotype.Component;

/**
 * `--spring.profiles.active=migrate`: apply Flyway migrations, then exit.
 *
 * In Kubernetes this runs as a pre-install/pre-upgrade Helm hook Job: the only workload that
 * holds admin database credentials. The long-running ingestion pods use the RLS-restricted app
 * role with Flyway disabled.
 */
@Component
@Profile("migrate")
class MigrateOnly implements CommandLineRunner {

    private final ApplicationContext context;

    MigrateOnly(ApplicationContext context) {
        this.context = context;
    }

    @Override
    public void run(String... args) {
        System.exit(SpringApplication.exit(context, () -> 0));
    }
}
