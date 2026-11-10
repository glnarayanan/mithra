package app

import "testing"

func TestGroupDigitsFollowsLocale(t *testing.T) {
	cases := []struct{ value, locale, want string }{
		{"735000", "en-IN", "7,35,000"},
		{"32000000", "en-IN", "3,20,00,000"},
		{"-1250.50", "en-IN", "−1,250.50"},
		{"999", "en-IN", "999"},
		{"735000", "en-GB", "735,000"},
		{"32000000", "", "32,000,000"},
	}
	for _, c := range cases {
		if got := groupDigitsIn(c.value, c.locale); got != c.want {
			t.Errorf("groupDigitsIn(%q, %q) = %q, want %q", c.value, c.locale, got, c.want)
		}
	}
}

func TestMoneyShowsTheHouseholdSign(t *testing.T) {
	cases := []struct{ value, locale, currency, want string }{
		{"735000", "en-IN", "INR", "₹7,35,000"},
		{"-1250.50", "en-GB", "GBP", "−£1,250.50"},
		{"735000", "", "", "735,000"},
		{"", "en-IN", "INR", ""},
	}
	for _, c := range cases {
		if got := moneyIn(c.value, c.locale, c.currency); got != c.want {
			t.Errorf("moneyIn(%q, %q, %q) = %q, want %q", c.value, c.locale, c.currency, got, c.want)
		}
	}
}
