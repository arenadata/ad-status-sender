package importadcm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
	"github.com/arenadata/ad-status-sender/internal/rules"
	"github.com/arenadata/ad-status-sender/internal/rulesimport"
	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
)

func FromADCM(ctx context.Context, tx *sql.Tx, client *adcmclient.Client, hostID int, log *slog.Logger) error {
	if client == nil {
		return errors.New("adcm client is nil")
	}
	if log == nil {
		log = slog.Default()
	}
	if hostID == 0 {
		return errors.New("hostID must be non-zero")
	}

	host, clusterID, err := fetchHost(ctx, client, hostID)
	if err != nil {
		return err
	}
	if _, ehErr := sqlite.EnsureHostTx(ctx, tx, hostID, host.Name); ehErr != nil {
		return fmt.Errorf("ensure host %d: %w", hostID, ehErr)
	}

	unitToTargets := make(map[string][]rules.ComponentTarget)
	// Primary row: components post under the scoped host, so tag with host_id 0.
	if rErr := addRow(ctx, client, clusterID, host.Components, 0, unitToTargets, log); rErr != nil {
		return rErr
	}
	// Shared-host duplicates live in other clusters and get NO server-side
	// component fan-out; enumerate each and tag its components with its own id.
	for _, dup := range host.Duplicates {
		if dErr := addDuplicateRow(ctx, client, dup.ID, unitToTargets, log); dErr != nil {
			return dErr
		}
	}
	return persistRules(ctx, tx, hostID, unitToTargets)
}

// addRow expands one host's cluster services into unit -> component targets,
// tagging every component with hostTag (0 for the primary, the duplicate id
// otherwise).
func addRow(
	ctx context.Context,
	client *adcmclient.Client,
	clusterID int,
	comps []adcmclient.ComponentShort,
	hostTag int,
	unitToTargets map[string][]rules.ComponentTarget,
	log *slog.Logger,
) error {
	hostComps := hostComponentSet(comps)
	unitToComps, err := collectSystemdRules(ctx, client, clusterID, hostComps, log)
	if err != nil {
		return err
	}
	for unit, ids := range unitToComps {
		for _, id := range ids {
			unitToTargets[unit] = append(unitToTargets[unit], rules.ComponentTarget{HostID: hostTag, ComponentID: id})
		}
	}
	return nil
}

// addDuplicateRow enumerates one duplicate. A permanent condition (host gone,
// forbidden, or unclustered) is skipped, since dropping its targets is correct.
// A transient failure (5xx / network) is propagated so the whole import rolls
// back: FromADCM replaces the rule set, and committing a partial snapshot would
// silently wipe a still-valid duplicate's targets until the next successful sync.
func addDuplicateRow(
	ctx context.Context,
	client *adcmclient.Client,
	dupID int,
	unitToTargets map[string][]rules.ComponentTarget,
	log *slog.Logger,
) error {
	dup, err := client.GetHost(ctx, dupID)
	if err != nil {
		if permanentFetchError(err) {
			log.WarnContext(ctx, "adcm shared-host duplicate skipped: fetch failed", "duplicate", dupID, "err", err)
			return nil
		}
		return fmt.Errorf("fetch duplicate %d: %w", dupID, err)
	}
	if dup.Cluster == nil {
		log.WarnContext(ctx, "adcm shared-host duplicate skipped: no cluster", "duplicate", dupID)
		return nil
	}
	if rErr := addRow(ctx, client, dup.Cluster.ID, dup.Components, dupID, unitToTargets, log); rErr != nil {
		return fmt.Errorf("duplicate %d rules: %w", dupID, rErr)
	}
	return nil
}

// permanentFetchError is true for 404 (deleted/un-shared) and 403 (no RBAC
// visibility); both mean the duplicate's targets should be dropped, not retried.
func permanentFetchError(err error) bool {
	code, ok := adcmclient.HTTPStatusCode(err)
	return ok && (code == http.StatusNotFound || code == http.StatusForbidden)
}

func parseSystemdUnit(raw string, log *slog.Logger, serviceName, componentName string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		log.Warn("adcm component config parse failed", "service", serviceName, "component", componentName, "err", err)
		return "", false
	}
	sys, ok := doc["systemd"].(map[string]any)
	if !ok {
		return "", false
	}
	val, ok := sys["service_name"].(string)
	if !ok {
		return "", false
	}
	name := strings.TrimSpace(val)
	if name == "" {
		return "", false
	}
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	return name, true
}

func fetchHost(ctx context.Context, client *adcmclient.Client, hostID int) (*adcmclient.HostObject, int, error) {
	host, err := client.GetHost(ctx, hostID)
	if err != nil {
		return nil, 0, err
	}
	if host.Cluster == nil {
		return nil, 0, fmt.Errorf("host %d has no cluster assigned", hostID)
	}
	return host, host.Cluster.ID, nil
}

func hostComponentSet(comps []adcmclient.ComponentShort) map[int]struct{} {
	out := make(map[int]struct{}, len(comps))
	for _, c := range comps {
		out[c.ID] = struct{}{}
	}
	return out
}

func collectSystemdRules(
	ctx context.Context,
	client *adcmclient.Client,
	clusterID int,
	hostComps map[int]struct{},
	log *slog.Logger,
) (map[string][]string, error) {
	services, err := client.ListClusterServices(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	unitToComps := make(map[string][]string)
	for _, svc := range services {
		if svcErr := addServiceRules(ctx, client, clusterID, svc, hostComps, log, unitToComps); svcErr != nil {
			return nil, svcErr
		}
	}
	return unitToComps, nil
}

// addServiceRules maps this service's systemd components to the host. Component
// names are resolved to ids via the service's own component list, so a name
// reused by another service (e.g. "historyserver") cannot attach a foreign
// unit; only components actually mapped to the host are kept.
func addServiceRules(
	ctx context.Context,
	client *adcmclient.Client,
	clusterID int,
	svc adcmclient.ServiceObject,
	hostComps map[int]struct{},
	log *slog.Logger,
	unitToComps map[string][]string,
) error {
	cfgID, cfgErr := client.CurrentServiceConfigID(ctx, clusterID, svc.ID)
	if cfgErr != nil {
		return cfgErr
	}
	if cfgID == 0 {
		return nil
	}
	cfg, cfgErr := client.GetServiceConfig(ctx, clusterID, svc.ID, cfgID)
	if cfgErr != nil {
		return cfgErr
	}
	if len(cfg.Components) == 0 {
		return nil
	}
	svcComps, listErr := client.ListServiceComponents(ctx, clusterID, svc.ID)
	if listErr != nil {
		return listErr
	}
	nameToID := make(map[string]int, len(svcComps))
	for _, sc := range svcComps {
		nameToID[sc.Name] = sc.ID
	}
	for compName, raw := range cfg.Components {
		compID, ok := nameToID[compName]
		if !ok {
			continue
		}
		if _, onHost := hostComps[compID]; !onHost {
			continue
		}
		unit, ok := parseSystemdUnit(raw, log, svc.Name, compName)
		if !ok {
			continue
		}
		unitToComps[unit] = append(unitToComps[unit], strconv.Itoa(compID))
	}
	return nil
}

func persistRules(
	ctx context.Context,
	tx *sql.Tx,
	hostID int,
	unitToTargets map[string][]rules.ComponentTarget,
) error {
	for unit, targets := range unitToTargets {
		targets = dedupTargets(targets)
		if len(targets) == 0 {
			continue
		}
		ruleName := "adcm/" + unit
		ruleID, upErr := sqlite.UpsertSystemdRuleTx(ctx, tx, ruleName, true, unit, "")
		if upErr != nil {
			return upErr
		}
		if setErr := sqlite.SetRuleComponentTargetsTx(ctx, tx, ruleID, targets); setErr != nil {
			return setErr
		}
		// Rules are always scoped to the original host: the daemon loads by the
		// configured (original) id; duplicate ids ride along as component targets.
		if scErr := rulesimport.ApplyHostScope(ctx, tx, ruleID, []int{hostID}); scErr != nil {
			return scErr
		}
	}
	return nil
}

func dedupTargets(in []rules.ComponentTarget) []rules.ComponentTarget {
	seen := make(map[rules.ComponentTarget]struct{}, len(in))
	out := make([]rules.ComponentTarget, 0, len(in))
	for _, t := range in {
		if t.ComponentID == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
