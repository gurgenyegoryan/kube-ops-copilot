package approval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Record struct {
	ApprovalID string    `json:"approvalId"`
	Provider   Provider  `json:"provider"`
	CreatedAt  time.Time `json:"createdAt"`

	Summary   string `json:"summary"`
	Operation string `json:"operation"`
	Target    string `json:"target"`

	Decision   Decision  `json:"decision"`
	DecidedAt  time.Time `json:"decidedAt,omitempty"`
	DecidedBy  string    `json:"decidedBy,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Raw        string    `json:"raw,omitempty"`
}

type storeFile struct {
	Records []Record `json:"records"`
}

func defaultStorePath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return "./.kube-ops-copilot/approvals.json"
	}
	return filepath.Join(base, "kube-ops-copilot", "approvals.json")
}

func loadStore(path string) (storeFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return storeFile{}, nil
		}
		return storeFile{}, err
	}
	var sf storeFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return storeFile{}, err
	}
	return sf, nil
}

func saveStore(path string, sf storeFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func upsertRecord(path string, r Record) error {
	sf, err := loadStore(path)
	if err != nil {
		return err
	}
	for i := range sf.Records {
		if sf.Records[i].ApprovalID == r.ApprovalID {
			sf.Records[i] = r
			return saveStore(path, sf)
		}
	}
	sf.Records = append(sf.Records, r)
	return saveStore(path, sf)
}

func getRecord(path, approvalID string) (Record, bool, error) {
	sf, err := loadStore(path)
	if err != nil {
		return Record{}, false, err
	}
	for _, r := range sf.Records {
		if r.ApprovalID == approvalID {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

func updateDecision(path, approvalID string, decision Decision, by, reason, raw string) error {
	r, ok, err := getRecord(path, approvalID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown approval id: %s", approvalID)
	}
	r.Decision = decision
	r.DecidedAt = time.Now().UTC()
	r.DecidedBy = by
	r.Reason = reason
	r.Raw = raw
	return upsertRecord(path, r)
}
