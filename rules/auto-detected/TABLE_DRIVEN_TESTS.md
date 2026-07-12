# Table-Driven Tests

> Auto-detected by /spartan:scan-rules — review and edit as needed.

Use table-driven tests with named subtests. Each test case is a struct with a descriptive `name` field. Run subtests with `t.Run()` for better output and isolation.

## CORRECT

```go
func TestValidateCreateAppointment(t *testing.T) {
    tests := []struct {
        name           string
        assessmentID   string
        scheduledDate  string
        scheduledTime  string
        wantOk         bool
    }{
        {"valid appointment", "assess-123", "2026-07-10", "10:00", true},
        {"empty assessment ID", "", "2026-07-10", "10:00", false},
        {"empty date", "assess-123", "", "10:00", false},
        {"empty time", "assess-123", "2026-07-10", "", false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := ValidateCreateAppointment(tt.assessmentID, tt.scheduledDate, tt.scheduledTime)
            if tt.wantOk && err != nil {
                t.Errorf("expected no error, got %v", err)
            }
            if !tt.wantOk && err == nil {
                t.Error("expected error, got nil")
            }
        })
    }
}
```

## WRONG

```go
func TestValidateCreateAppointment(t *testing.T) {
    // No table, multiple t.Run calls with duplicated logic
    t.Run("valid", func(t *testing.T) {
        err := ValidateCreateAppointment("id", "2026-07-10", "10:00")
        if err != nil {
            t.Error(err)
        }
    })
    t.Run("empty id", func(t *testing.T) {
        err := ValidateCreateAppointment("", "2026-07-10", "10:00")
        if err == nil {
            t.Error("expected error")
        }
    })
    // ... repeated logic
}
```

## Quick Reference

| Aspect | Convention |
|--------|-----------|
| Test Structure | Slice of anonymous structs |
| Name Field | `name string` — descriptive test case name |
| Subtest Execution | `t.Run(tt.name, func(t *testing.T) { ... })` |
| Assertions | Use `t.Error`, `t.Errorf`, `t.Fatal`, `t.Fatalf` |
| Test File | Co-located: `foo.go` → `foo_test.go` |
