// Package health implements factual, source-linked health observations.
package health

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

func ParseValue(input string) (Value, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return Value{}, ErrValueRange
	}
	negative := false
	if strings.HasPrefix(raw, "-") {
		negative, raw = true, raw[1:]
	} else if strings.HasPrefix(raw, "+") {
		raw = raw[1:]
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" || !decimalDigits(parts[0]) {
		return Value{}, ErrValueRange
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > maxScale || !decimalDigits(fraction) {
			return Value{}, ErrValueRange
		}
	}
	digits := strings.TrimLeft(parts[0]+fraction, "0")
	if digits == "" {
		digits = "0"
	}
	coefficient, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || coefficient > maxCoefficient {
		return Value{}, ErrValueRange
	}
	if negative && coefficient != 0 {
		coefficient = -coefficient
	}
	return Value{Coefficient: coefficient, Scale: uint8(len(fraction))}, nil
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

const (
	maxScale       = 6
	maxCoefficient = int64(9_000_000_000_000_000)
)

var (
	ErrUnitMissing      = errors.New("unit is missing")
	ErrUnitIncompatible = errors.New("units are not explicitly compatible")
	ErrValueRange       = errors.New("value is outside supported bounds")
)

type Value struct {
	Coefficient int64
	Scale       uint8
}

func (value Value) PlainString() string {
	negative := value.Coefficient < 0
	coefficient := value.Coefficient
	if negative {
		coefficient = -coefficient
	}
	digits := strconv.FormatInt(coefficient, 10)
	if value.Scale > 0 {
		width := int(value.Scale) + 1
		if len(digits) < width {
			digits = strings.Repeat("0", width-len(digits)) + digits
		}
		point := len(digits) - int(value.Scale)
		digits = digits[:point] + "." + digits[point:]
	}
	if negative && value.Coefficient != 0 {
		return "-" + digits
	}
	return digits
}

type unitDefinition struct {
	canonical   string
	family      string
	numerator   int64
	denominator int64
}

var compatibleUnits = map[string]unitDefinition{
	"g/l":   {canonical: "mg/dL", family: "mass-concentration-a", numerator: 100, denominator: 1},
	"mg/dl": {canonical: "mg/dL", family: "mass-concentration-a", numerator: 1, denominator: 1},
	"mg/l":  {canonical: "mg/L", family: "mass-concentration-b", numerator: 1, denominator: 1},
	"ug/ml": {canonical: "mg/L", family: "mass-concentration-b", numerator: 1, denominator: 1},
	"µg/ml": {canonical: "mg/L", family: "mass-concentration-b", numerator: 1, denominator: 1},
	"μg/ml": {canonical: "mg/L", family: "mass-concentration-b", numerator: 1, denominator: 1},
	"kg":    {canonical: "kg", family: "mass", numerator: 1, denominator: 1},
	"g":     {canonical: "kg", family: "mass", numerator: 1, denominator: 1000},
	"cm":    {canonical: "cm", family: "length", numerator: 1, denominator: 1},
	"mm":    {canonical: "cm", family: "length", numerator: 1, denominator: 10},
}

type ComparableUnit struct {
	Canonical string
	Family    string
}

func UnitFor(input string) (ComparableUnit, error) {
	normalized := normalizeUnit(input)
	if normalized == "" {
		return ComparableUnit{}, ErrUnitMissing
	}
	if definition, ok := compatibleUnits[normalized]; ok {
		return ComparableUnit{Canonical: definition.canonical, Family: definition.family}, nil
	}
	return ComparableUnit{Canonical: strings.TrimSpace(input), Family: "exact:" + normalized}, nil
}

func Convert(value Value, from, to string) (Value, error) {
	fromDefinition, fromKnown := compatibleUnits[normalizeUnit(from)]
	toDefinition, toKnown := compatibleUnits[normalizeUnit(to)]
	if !fromKnown || !toKnown {
		if normalizeUnit(from) == "" || normalizeUnit(to) == "" {
			return Value{}, ErrUnitMissing
		}
		if normalizeUnit(from) != normalizeUnit(to) {
			return Value{}, ErrUnitIncompatible
		}
		return validateValue(value)
	}
	if fromDefinition.family != toDefinition.family {
		return Value{}, ErrUnitIncompatible
	}
	coefficient, ok := multiplyBounded(value.Coefficient, fromDefinition.numerator)
	if !ok {
		return Value{}, ErrValueRange
	}
	denominator := fromDefinition.denominator * toDefinition.numerator
	numerator := toDefinition.denominator
	coefficient, ok = multiplyBounded(coefficient, numerator)
	if !ok {
		return Value{}, ErrValueRange
	}
	scale := value.Scale
	for coefficient%denominator != 0 && scale < maxScale {
		coefficient, ok = multiplyBounded(coefficient, 10)
		if !ok {
			return Value{}, ErrValueRange
		}
		scale++
	}
	if denominator == 0 || coefficient%denominator != 0 {
		return Value{}, ErrValueRange
	}
	return validateValue(Value{Coefficient: coefficient / denominator, Scale: scale})
}

func ComparabilityKey(analyte, subject, specimen, method, referenceContext, unit string) (string, error) {
	comparable, err := UnitFor(unit)
	if err != nil {
		return "", err
	}
	parts := []string{analyte, subject, specimen, method, referenceContext, comparable.Family, comparable.Canonical}
	for index := range parts {
		parts[index] = strings.ToLower(strings.Join(strings.Fields(parts[index]), " "))
	}
	if parts[0] == "" || parts[1] == "" {
		return "", errors.New("analyte and subject are required")
	}
	return strings.Join(parts, "\x1f"), nil
}

func normalizeUnit(unit string) string {
	return strings.ToLower(strings.ReplaceAll(strings.Join(strings.Fields(unit), ""), "μ", "µ"))
}

func multiplyBounded(value, factor int64) (int64, bool) {
	if factor <= 0 || value > 0 && value > maxCoefficient/factor || value < 0 && value < -maxCoefficient/factor {
		return 0, false
	}
	return value * factor, true
}

func validateValue(value Value) (Value, error) {
	if value.Scale > maxScale || value.Coefficient > maxCoefficient || value.Coefficient < -maxCoefficient || value.Coefficient == math.MinInt64 {
		return Value{}, ErrValueRange
	}
	return value, nil
}
