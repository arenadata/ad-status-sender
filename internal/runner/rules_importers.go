package runner

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/arenadata/ad-status-sender/internal/adcmclient"
	"github.com/arenadata/ad-status-sender/internal/importadcm"
	"github.com/arenadata/ad-status-sender/internal/importlegacy"
	"github.com/arenadata/ad-status-sender/internal/importrules"
	"github.com/arenadata/ad-status-sender/internal/rules"
)

type rulesImporter interface {
	Import(ctx context.Context, tx *sql.Tx) error
}

type yamlFileImporter struct {
	path string
}

func (i yamlFileImporter) Import(ctx context.Context, tx *sql.Tx) error {
	rr, err := rules.Load(i.path)
	if err != nil {
		return err
	}
	return importrules.Rules(ctx, tx, rr, nil)
}

type yamlRulesImporter struct {
	rr rules.Rules
}

func (i yamlRulesImporter) Import(ctx context.Context, tx *sql.Tx) error {
	return importrules.Rules(ctx, tx, i.rr, nil)
}

type legacyImporter struct {
	legacyDir string
	hostID    int
}

func (i legacyImporter) Import(ctx context.Context, tx *sql.Tx) error {
	servicesDir := filepath.Join(i.legacyDir, "services")
	dockerDir := filepath.Join(i.legacyDir, "docker")
	hostsDir := filepath.Join(i.legacyDir, "hosts")
	hostIDs, err := legacyHostIDs(hostsDir)
	if err != nil {
		return err
	}
	if len(hostIDs) == 0 && i.hostID != 0 {
		hostIDs = []int{i.hostID}
	}
	if svcErr := importlegacy.ServicesDir(ctx, tx, servicesDir, "legacy/", hostIDs); svcErr != nil {
		return svcErr
	}
	return importlegacy.DockerDir(ctx, tx, dockerDir, "legacy/", hostIDs)
}

type adcmImporter struct {
	client *adcmclient.Client
	hostID int
}

func (i adcmImporter) Import(ctx context.Context, tx *sql.Tx) error {
	if i.client == nil {
		return errors.New("adcm client not initialized")
	}
	return importadcm.FromADCM(ctx, tx, i.client, i.hostID, nil)
}
