package health

import (
	"errors"
	"testing"
)

func TestExplicitCompatibleConversionsRemainExact(t *testing.T) {
	tests := []struct {
		value    Value
		from, to string
		want     string
	}{
		{Value{Coefficient: 125, Scale: 2}, "g/L", "mg/dL", "125.00"},
		{Value{Coefficient: 12500, Scale: 2}, "mg/dL", "g/L", "1.25"},
		{Value{Coefficient: 725, Scale: 2}, "kg", "g", "7250.00"},
		{Value{Coefficient: 7250, Scale: 1}, "g", "kg", "0.725"},
		{Value{Coefficient: 1800, Scale: 1}, "mm", "cm", "18.0"},
	}
	for _, test := range tests {
		got, err := Convert(test.value, test.from, test.to)
		if err != nil || got.PlainString() != test.want {
			t.Errorf("Convert(%s, %s, %s) = %s, %v; want %s", test.value.PlainString(), test.from, test.to, got.PlainString(), err, test.want)
		}
	}
}

func TestUnknownOrAnalyteSpecificMismatchRequiresCorrection(t *testing.T) {
	value := Value{Coefficient: 55, Scale: 1}
	if _, err := Convert(value, "mg/dL", "mmol/L"); !errors.Is(err, ErrUnitIncompatible) {
		t.Fatalf("analyte-specific conversion error = %v", err)
	}
	if _, err := Convert(value, "IU/L", "U/L"); !errors.Is(err, ErrUnitIncompatible) {
		t.Fatalf("unknown mismatch error = %v", err)
	}
	if got, err := Convert(value, "IU/L", " iu/l "); err != nil || got != value {
		t.Fatalf("unknown identity = %#v, %v", got, err)
	}
	if _, err := UnitFor(" "); !errors.Is(err, ErrUnitMissing) {
		t.Fatalf("missing unit error = %v", err)
	}
}

func TestComparabilityKeySeparatesClinicalContext(t *testing.T) {
	base, err := ComparabilityKey("Glucose", "Alex", "serum", "hexokinase", "report-a", "g/L")
	if err != nil {
		t.Fatal(err)
	}
	compatible, err := ComparabilityKey(" glucose ", "alex", "serum", "hexokinase", "report-a", "mg/dL")
	if err != nil || compatible != base {
		t.Fatalf("compatible key = %q, %v; want %q", compatible, err, base)
	}
	for _, changed := range []struct{ specimen, method, reference string }{{"plasma", "hexokinase", "report-a"}, {"serum", "oxidase", "report-a"}, {"serum", "hexokinase", "report-b"}} {
		key, err := ComparabilityKey("Glucose", "Alex", changed.specimen, changed.method, changed.reference, "mg/dL")
		if err != nil {
			t.Fatal(err)
		}
		if key == base {
			t.Fatalf("changed clinical context shared key %q", key)
		}
	}
}
