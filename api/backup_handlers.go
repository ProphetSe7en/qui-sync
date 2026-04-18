package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prophetse7en/qui-sync/core"
)

// Backups are stored at /data/backups-full/<instance-id>/<timestamp>/
// Each backup is a directory of JSON files — one per rule, full 1:1 copy
// from Qui with ALL fields (trackers, enabled, intervals, everything).

func backupsFullDir(cfg *core.Config) string {
	return filepath.Join(filepath.Dir(cfg.Paths().Repo), "backups-full")
}

func instanceBackupDir(cfg *core.Config, instanceID int) string {
	return filepath.Join(backupsFullDir(cfg), strconv.Itoa(instanceID))
}

// handleCreateBackup dumps all automations from a Qui instance into a
// timestamped directory. Full 1:1 — no stripping, no placeholders.
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	instanceID, err := strconv.Atoi(idStr)
	if err != nil || instanceID <= 0 {
		writeErr(w, 400, fmt.Errorf("invalid instance id %q", idStr))
		return
	}

	cfg := s.getConfig()
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	rules, err := client.ListAutomations(r.Context(), instanceID)
	if err != nil {
		writeErr(w, 502, err)
		return
	}

	ts := time.Now().Format("2006-01-02_15-04-05")
	dir := filepath.Join(instanceBackupDir(cfg, instanceID), ts)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, 500, err)
		return
	}

	for _, rule := range rules {
		data, err := json.MarshalIndent(json.RawMessage(rule.Raw), "", "  ")
		if err != nil {
			data = rule.Raw
		}
		slug := core.Slugify(rule.Name)
		fname := fmt.Sprintf("%s_%d.json", slug, rule.ID)
		if err := os.WriteFile(filepath.Join(dir, fname), data, 0o644); err != nil {
			writeErr(w, 500, fmt.Errorf("write %s: %w", fname, err))
			return
		}
	}

	writeJSON(w, 200, map[string]any{
		"instance_id": instanceID,
		"timestamp":   ts,
		"rule_count":  len(rules),
	})
}

type backupSummary struct {
	ID         string `json:"id"` // <instance-id>/<timestamp>
	InstanceID int    `json:"instance_id"`
	Timestamp  string `json:"timestamp"`
	RuleCount  int    `json:"rule_count"`
}

// handleListBackups returns all backups grouped by instance.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	baseDir := backupsFullDir(cfg)

	result := map[int][]backupSummary{}

	instanceDirs, err := os.ReadDir(baseDir)
	if os.IsNotExist(err) {
		writeJSON(w, 200, result)
		return
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	for _, id := range instanceDirs {
		if !id.IsDir() {
			continue
		}
		instID, err := strconv.Atoi(id.Name())
		if err != nil {
			continue
		}
		tsDirs, err := os.ReadDir(filepath.Join(baseDir, id.Name()))
		if err != nil {
			continue
		}
		for _, ts := range tsDirs {
			if !ts.IsDir() {
				continue
			}
			files, _ := os.ReadDir(filepath.Join(baseDir, id.Name(), ts.Name()))
			count := 0
			for _, f := range files {
				if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
					count++
				}
			}
			result[instID] = append(result[instID], backupSummary{
				ID:         id.Name() + "/" + ts.Name(),
				InstanceID: instID,
				Timestamp:  ts.Name(),
				RuleCount:  count,
			})
		}
		// Newest first.
		sort.Slice(result[instID], func(i, j int) bool {
			return result[instID][i].Timestamp > result[instID][j].Timestamp
		})
	}

	writeJSON(w, 200, result)
}

type restoreReq struct {
	InstanceID       int    `json:"instance_id"` // source instance of the backup
	Timestamp        string `json:"timestamp"`
	TargetInstanceID int    `json:"target_instance_id"` // where to restore to
}

// handleRestoreBackup reads a backup and creates/updates rules in the
// target Qui instance. Full restore — all fields including trackers.
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	var req restoreReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.InstanceID == 0 || req.Timestamp == "" || req.TargetInstanceID == 0 {
		writeErr(w, 400, fmt.Errorf("instance_id, timestamp, and target_instance_id are required"))
		return
	}

	cfg := s.getConfig()
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	dir := filepath.Join(instanceBackupDir(cfg, req.InstanceID), req.Timestamp)
	files, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, 404, fmt.Errorf("backup not found: %w", err))
		return
	}

	// Get existing rules in target to match by name.
	liveRules, err := client.ListAutomations(r.Context(), req.TargetInstanceID)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	liveByName := map[string]int{}
	for _, lr := range liveRules {
		liveByName[strings.TrimSpace(lr.Name)] = lr.ID
	}

	created, updated, failed := 0, 0, 0
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			failed++
			continue
		}

		// Parse name for matching.
		var parsed struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(data, &parsed)

		// Strip server-assigned fields so Qui accepts the payload.
		var obj map[string]any
		if json.Unmarshal(data, &obj) != nil {
			failed++
			continue
		}
		for _, k := range []string{"id", "instanceId", "createdAt", "updatedAt"} {
			delete(obj, k)
		}
		payload, _ := json.Marshal(obj)

		if existingID, ok := liveByName[strings.TrimSpace(parsed.Name)]; ok {
			// Update existing rule.
			if err := client.UpdateAutomation(r.Context(), req.TargetInstanceID, existingID, payload); err != nil {
				failed++
			} else {
				updated++
			}
		} else {
			// Create new rule.
			if _, err := client.CreateAutomation(r.Context(), req.TargetInstanceID, payload); err != nil {
				failed++
			} else {
				created++
			}
		}
	}

	writeJSON(w, 200, map[string]any{
		"created": created,
		"updated": updated,
		"failed":  failed,
	})
}
