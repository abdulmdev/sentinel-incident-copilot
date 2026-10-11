package dev.sentinel.gateway.alert;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import java.time.OffsetDateTime;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

import dev.sentinel.gateway.incident.IncidentRepository;
import dev.sentinel.gateway.incident.IncidentService;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

class AlertServiceTest {

    final IncidentService incidents = mock(IncidentService.class);
    final IncidentRepository records = mock(IncidentRepository.class);
    final AlertLinkRepository links = mock(AlertLinkRepository.class);
    final SimpleMeterRegistry meters = new SimpleMeterRegistry();
    final AlertService service = new AlertService(incidents, records, links, meters);

    static final OffsetDateTime STARTS = OffsetDateTime.parse("2026-10-06T10:00:00Z");
    static final UUID ID = UUID.fromString("00000000-0000-0000-0000-000000000001");

    static AlertmanagerWebhook.Alert firing(Map<String, String> labels) {
        return new AlertmanagerWebhook.Alert("firing", labels,
                Map.of("summary", "checkout 5xx ratio above 20%"), STARTS, null, "fp-1");
    }

    static AlertmanagerWebhook webhook(AlertmanagerWebhook.Alert... alerts) {
        return new AlertmanagerWebhook("4", "firing", "sentinel", List.of(alerts));
    }

    void linkAs(AlertLinkRepository.Outcome outcome) {
        when(links.link(any(), any(), any(), any(), any())).thenAnswer(inv -> {
            if (outcome == AlertLinkRepository.Outcome.OPENED) {
                return new AlertLinkRepository.Link(inv.<java.util.function.Supplier<UUID>>getArgument(4).get(), outcome);
            }
            return new AlertLinkRepository.Link(ID, outcome);
        });
    }

    @Test
    void newAlertOpensAnIncidentDatedWhenTheProblemBegan() {
        when(incidents.record(any())).thenReturn(ID);
        linkAs(AlertLinkRepository.Outcome.OPENED);

        var result = service.handle("acme", webhook(firing(Map.of(
                "alertname", "CheckoutErrors", "service", "checkout", "severity", "critical", "tenant_id", "globex"))));

        var captor = ArgumentCaptor.forClass(IncidentService.NewIncident.class);
        verify(incidents).record(captor.capture());
        var n = captor.getValue();
        assertThat(n.tenant()).isEqualTo("acme"); // from the token; the label is ignored
        assertThat(n.service()).isEqualTo("checkout");
        assertThat(n.severity()).isEqualTo("SEV1");
        assertThat(n.openedAt()).isEqualTo(STARTS);
        assertThat(n.source()).isEqualTo("alertmanager");
        assertThat(n.openedBy()).isEqualTo("alertmanager:CheckoutErrors");
        verify(links).link(eq("acme"), eq("checkout"), eq("fp-1"), eq("CheckoutErrors"), any());
        verify(incidents).investigateInBackground(n, ID);
        assertThat(result.opened()).containsExactly(ID.toString());
        assertThat(meters.counter("sentinel.alerts", "outcome", "opened").count()).isEqualTo(1);
    }

    @Test
    void repeatedNotificationIsDeduplicatedOntoTheActiveIncident() {
        linkAs(AlertLinkRepository.Outcome.DEDUPLICATED);

        var result = service.handle("acme", webhook(firing(Map.of("alertname", "X", "service", "checkout"))));

        assertThat(result.deduplicated()).isEqualTo(1);
        assertThat(result.opened()).isEmpty();
        verify(incidents, never()).record(any());
        verify(incidents, never()).investigateInBackground(any(), any());
    }

    @Test
    void secondRuleOnTheSameServiceJoinsTheOpenIncidentWithoutANewInvestigation() {
        linkAs(AlertLinkRepository.Outcome.CORRELATED);

        var result = service.handle("acme", webhook(firing(Map.of("alertname", "DbPoolSaturated", "service", "checkout"))));

        assertThat(result.correlated()).isEqualTo(1);
        assertThat(result.opened()).isEmpty();
        verify(incidents, never()).investigateInBackground(any(), any());
        assertThat(meters.counter("sentinel.alerts", "outcome", "correlated").count()).isEqualTo(1);
    }

    @Test
    void alertsWithoutAValidServiceLabelAreSkipped() {
        var result = service.handle("acme", webhook(
                firing(Map.of("alertname", "NoService")),
                firing(Map.of("alertname", "Traversal", "service", "../etc"))));

        assertThat(result.skipped()).hasSize(2);
        verify(incidents, never()).record(any());
    }

    @Test
    void resolvedAlertIsRecordedOnTheLinkedIncident() {
        var endsAt = STARTS.plusMinutes(7);
        when(links.resolve("acme", "fp-1", endsAt)).thenReturn(Optional.of(ID));

        var result = service.handle("acme", webhook(new AlertmanagerWebhook.Alert("resolved",
                Map.of("alertname", "X", "service", "checkout"), Map.of(), STARTS, endsAt, "fp-1")));

        assertThat(result.resolved()).isEqualTo(1);
        verify(records).markAlertResolved("acme", ID, endsAt);
        verify(incidents, never()).record(any());
    }

    @Test
    void missingFingerprintFallsBackToAStableLabelHash() {
        linkAs(AlertLinkRepository.Outcome.DEDUPLICATED);
        var labels = Map.of("alertname", "X", "service", "checkout");
        service.handle("acme", webhook(new AlertmanagerWebhook.Alert("firing", labels, null, STARTS, null, null)));
        service.handle("acme", webhook(new AlertmanagerWebhook.Alert("firing", Map.copyOf(labels), null, STARTS, null, "")));

        var fp = ArgumentCaptor.forClass(String.class);
        verify(links, org.mockito.Mockito.times(2)).link(eq("acme"), eq("checkout"), fp.capture(), eq("X"), any());
        assertThat(fp.getAllValues().get(0)).isNotBlank().isEqualTo(fp.getAllValues().get(1));
    }

    @Test
    void severityMapping() {
        assertThat(AlertService.severity("critical")).isEqualTo("SEV1");
        assertThat(AlertService.severity("warning")).isEqualTo("SEV2");
        assertThat(AlertService.severity("info")).isEqualTo("SEV3");
        assertThat(AlertService.severity(null)).isEqualTo("SEV2");
    }
}
