package admin

import (
	"encoding/csv"
	"fmt"
	"strings"
)

type ExportColumn struct {
	Header string
	Value  func(record map[string]interface{}) string
}

func ExportToCSV(records []map[string]interface{}, columns []ExportColumn) (string, error) {
	if len(records) == 0 {
		return "", fmt.Errorf("no records to export")
	}

	var buf strings.Builder
	writer := csv.NewWriter(&buf)

	headers := make([]string, len(columns))
	for i, col := range columns {
		headers[i] = col.Header
	}
	if err := writer.Write(headers); err != nil {
		return "", fmt.Errorf("failed to write CSV headers: %w", err)
	}

	for _, record := range records {
		row := make([]string, len(columns))
		for i, col := range columns {
			row[i] = col.Value(record)
		}
		if err := writer.Write(row); err != nil {
			return "", fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("failed to write CSV: %w", err)
	}

	return buf.String(), nil
}

func ExportToJSON(records []map[string]interface{}) (string, error) {
	if len(records) == 0 {
		return "", fmt.Errorf("no records to export")
	}
	return fmt.Sprintf("%v", records), nil
}
