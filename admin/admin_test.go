package admin

import (
	"testing"
)

func TestExportToCSV(t *testing.T) {
	records := []map[string]interface{}{
		{"name": "Alice", "email": "alice@example.com"},
		{"name": "Bob", "email": "bob@example.com"},
	}

	columns := []ExportColumn{
		{Header: "Name", Value: func(r map[string]interface{}) string { return r["name"].(string) }},
		{Header: "Email", Value: func(r map[string]interface{}) string { return r["email"].(string) }},
	}

	result, err := ExportToCSV(records, columns)
	if err != nil {
		t.Fatalf("ExportToCSV returned error: %v", err)
	}

	expected := "Name,Email\nAlice,alice@example.com\nBob,bob@example.com\n"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestExportToCSVEmpty(t *testing.T) {
	_, err := ExportToCSV([]map[string]interface{}{}, nil)
	if err == nil {
		t.Error("expected error for empty records, got nil")
	}
}
