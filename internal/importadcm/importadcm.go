package importadcm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
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
	compIDs := componentIDMap(host.Components)
	unitToComps, err := collectSystemdRules(ctx, client, clusterID, compIDs, log)
	if err != nil {
		return err
	}
	return persistRules(ctx, tx, hostID, unitToComps)
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

func componentIDMap(comps []adcmclient.ComponentShort) map[string]string {
	out := make(map[string]string, len(comps))
	for _, c := range comps {
		out[c.Name] = strconv.Itoa(c.ID)
	}
	return out
}

func collectSystemdRules(
	ctx context.Context,
	client *adcmclient.Client,
	clusterID int,
	compIDs map[string]string,
	log *slog.Logger,
) (map[string][]string, error) {
	services, err := client.ListClusterServices(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	unitToComps := make(map[string][]string)
	for _, svc := range services {
		if svcErr := addServiceRules(ctx, client, clusterID, svc, compIDs, log, unitToComps); svcErr != nil {
			return nil, svcErr
		}
	}
	return unitToComps, nil
}

func addServiceRules(
	ctx context.Context,
	client *adcmclient.Client,
	clusterID int,
	svc adcmclient.ServiceObject,
	compIDs map[string]string,
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
	for compName, raw := range cfg.Components {
		compID, ok := compIDs[compName]
		if !ok {
			continue
		}
		unit, ok := parseSystemdUnit(raw, log, svc.Name, compName)
		if !ok {
			continue
		}
		unitToComps[unit] = append(unitToComps[unit], compID)
	}
	return nil
}

func persistRules(
	ctx context.Context,
	tx *sql.Tx,
	hostID int,
	unitToComps map[string][]string,
) error {
	for unit, comps := range unitToComps {
		comps = rulesimport.DedupTrim(comps)
		if len(comps) == 0 {
			continue
		}
		ruleName := "adcm/" + unit
		ruleID, upErr := sqlite.UpsertSystemdRuleTx(ctx, tx, ruleName, true, unit, "")
		if upErr != nil {
			return upErr
		}
		if setErr := sqlite.SetRuleComponentsTx(ctx, tx, ruleID, comps); setErr != nil {
			return setErr
		}
		if scErr := rulesimport.ApplyHostScope(ctx, tx, ruleID, []int{hostID}); scErr != nil {
			return scErr
		}
	}
	return nil
}
