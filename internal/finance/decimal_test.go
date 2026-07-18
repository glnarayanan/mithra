package finance

import (
	"errors"
	"testing"
)

func TestParseAmountAcceptsExactNumberOnlyForms(t *testing.T) {
	tests := map[string]Decimal{
		"0":          {0, 0},
		" 12 ":       {12, 0},
		"1,234.50":   {123450, 2},
		"-0.125":     {-125, 3},
		"(2,400.00)": {-240000, 2},
		"+72.000001": {72000001, 6},
		"000,001.20": {120, 2},
	}
	for input, want := range tests {
		got, err := ParseAmount(input)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %#v, %v; want %#v", input, got, err, want)
		}
	}
}

func TestParseAmountSeparatesBlankInvalidAndOverflow(t *testing.T) {
	if _, err := ParseAmount("   "); !errors.Is(err, ErrBlank) {
		t.Fatalf("blank error = %v", err)
	}
	for _, input := range []string{"₹12", "$12", "EUR 12", "1,23", "1,234,56", "1.", ".2", "--2", "(-2)", "2.1234567", "NaN"} {
		if _, err := ParseAmount(input); !errors.Is(err, ErrAmount) {
			t.Errorf("ParseAmount(%q) error = %v", input, err)
		}
	}
	if _, err := ParseAmount("9,000,000,000,000,001"); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestExactSumAlignsScaleAndRendersPlainNumbers(t *testing.T) {
	values := []Decimal{{12345, 2}, {-125, 3}, {7, 0}, {50, 1}}
	total, err := Sum(values)
	if err != nil {
		t.Fatal(err)
	}
	if total != (Decimal{Coefficient: 135325, Scale: 3}) || total.PlainString() != "135.325" {
		t.Fatalf("sum = %#v (%s)", total, total.PlainString())
	}
	for value, want := range map[Decimal]string{{0, 2}: "0.00", {-5, 2}: "-0.05", {500, 3}: "0.500", {42, 0}: "42"} {
		if got := value.PlainString(); got != want {
			t.Errorf("%#v string = %q, want %q", value, got, want)
		}
	}
}

func TestExactSumRejectsBoundOverflow(t *testing.T) {
	if _, err := Add(Decimal{Coefficient: maxCoefficient, Scale: 0}, Decimal{Coefficient: 1, Scale: 0}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow add = %v", err)
	}
}
