package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// PerfStore persists host performance samples and request records to SQLite.
type PerfStore struct {
	db *sql.DB
}

func NewPerfStore(dbPath string) (*PerfStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open perf db: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %s: %w", p, err)
		}
	}

	schema := `
	CREATE TABLE IF NOT EXISTS perf_host_samples (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		host_idx     INTEGER NOT NULL,
		responsive   INTEGER NOT NULL,
		send_time    TEXT NOT NULL,
		receipt_time TEXT NOT NULL,
		first_token  TEXT NOT NULL,
		total_time_ms REAL NOT NULL,
		input_tokens INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS perf_request_log (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp       TEXT NOT NULL,
		input_tokens    INTEGER NOT NULL,
		winner_host_idx INTEGER NOT NULL,
		winner_nonce    INTEGER NOT NULL,
		decision        TEXT NOT NULL,
		hosts_json      TEXT NOT NULL
	);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create perf schema: %w", err)
	}

	return &PerfStore{db: db}, nil
}

func (s *PerfStore) Close() error {
	return s.db.Close()
}

func (s *PerfStore) InsertSample(sample RequestSample) error {
	_, err := s.db.Exec(
		`INSERT INTO perf_host_samples (host_idx, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sample.HostIdx,
		boolToInt(sample.Responsive),
		timeToStr(sample.SendTime),
		timeToStr(sample.ReceiptTime),
		timeToStr(sample.FirstToken),
		float64(sample.TotalTime.Milliseconds()),
		sample.InputTokens,
	)
	return err
}

func (s *PerfStore) InsertRequest(rec RequestRecord) error {
	hostsJSON, err := json.Marshal(rec.Hosts)
	if err != nil {
		return fmt.Errorf("marshal hosts: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO perf_request_log (timestamp, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		rec.Timestamp.Format(time.RFC3339Nano),
		rec.InputTokens,
		rec.WinnerHostIdx,
		rec.WinnerNonce,
		rec.Decision,
		string(hostsJSON),
	)
	return err
}

// LoadSamples returns the most recent perfWindowSize samples per host.
func (s *PerfStore) LoadSamples() ([]RequestSample, error) {
	rows, err := s.db.Query(
		`SELECT host_idx, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens
		 FROM perf_host_samples ORDER BY id DESC LIMIT ?`, perfWindowSize*128)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples []RequestSample
	for rows.Next() {
		var (
			hostIdx     int
			responsive  int
			sendStr     string
			receiptStr  string
			firstStr    string
			totalMs     float64
			inputTokens uint64
		)
		if err := rows.Scan(&hostIdx, &responsive, &sendStr, &receiptStr, &firstStr, &totalMs, &inputTokens); err != nil {
			return nil, err
		}
		samples = append(samples, RequestSample{
			HostIdx:     hostIdx,
			Responsive:  responsive != 0,
			SendTime:    strToTime(sendStr),
			ReceiptTime: strToTime(receiptStr),
			FirstToken:  strToTime(firstStr),
			TotalTime:   time.Duration(totalMs) * time.Millisecond,
			InputTokens: inputTokens,
		})
	}

	// Reverse so oldest-first (ring buffer expects chronological insert order).
	for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
		samples[i], samples[j] = samples[j], samples[i]
	}
	return samples, rows.Err()
}

// LoadRequests returns the most recent requestLogSize request records.
func (s *PerfStore) LoadRequests() ([]RequestRecord, error) {
	rows, err := s.db.Query(
		`SELECT timestamp, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json
		 FROM perf_request_log ORDER BY id DESC LIMIT ?`, requestLogSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []RequestRecord
	for rows.Next() {
		var (
			tsStr       string
			inputTokens uint64
			winnerIdx   int
			winnerNonce uint64
			decision    string
			hostsJSON   string
		)
		if err := rows.Scan(&tsStr, &inputTokens, &winnerIdx, &winnerNonce, &decision, &hostsJSON); err != nil {
			return nil, err
		}
		rec := RequestRecord{
			Timestamp:     strToTime(tsStr),
			InputTokens:   inputTokens,
			WinnerHostIdx: winnerIdx,
			WinnerNonce:   winnerNonce,
			Decision:      decision,
		}
		if err := json.Unmarshal([]byte(hostsJSON), &rec.Hosts); err != nil {
			return nil, fmt.Errorf("unmarshal hosts: %w", err)
		}
		records = append(records, rec)
	}

	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, rows.Err()
}

// Prune removes old rows beyond the retention window.
func (s *PerfStore) Prune() error {
	_, err := s.db.Exec(
		`DELETE FROM perf_host_samples WHERE id NOT IN (SELECT id FROM perf_host_samples ORDER BY id DESC LIMIT ?)`,
		perfWindowSize*128)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`DELETE FROM perf_request_log WHERE id NOT IN (SELECT id FROM perf_request_log ORDER BY id DESC LIMIT ?)`,
		requestLogSize)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func timeToStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func strToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
