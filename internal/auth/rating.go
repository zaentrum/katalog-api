package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sync"
)

// A kid's account is capped at an age: its access token carries the claim
// max_rating, a whole number of years, and the catalog serves it no title
// rated above that age. A token without the claim is not capped (an admin, an
// adult). One whose claim is no whole number of years is held to the
// strictest cap, 0, so a claim gone wrong shows less, never more, and the
// service says so once.

// MaxRatingClaim is the claim of an access token that caps its viewer at an
// age.
const MaxRatingClaim = "max_rating"

type ctxKey int

const maxRatingKey ctxKey = 0

var malformedCap sync.Once

// MaxRating is the age the verified bearer of ctx caps its viewer at; capped
// is false for a request without one (no claim, or no bearer verified).
func MaxRating(ctx context.Context) (age int, capped bool) {
	v, ok := ctx.Value(maxRatingKey).(int)
	return v, ok
}

// WithMaxRating is ctx for a viewer capped at age, as the middleware puts a
// verified bearer's max_rating on its request.
func WithMaxRating(ctx context.Context, age int) context.Context {
	return context.WithValue(ctx, maxRatingKey, age)
}

// withMaxRating puts the cap a token's claims carry on ctx: the claim's age,
// 0 for a claim that is no whole number of years, nothing without the claim.
func withMaxRating(ctx context.Context, raw json.RawMessage, present bool) context.Context {
	if !present {
		return ctx
	}
	age, ok := WholeYears(raw)
	if !ok {
		malformedCap.Do(func() {
			slog.Warn("a max_rating claim is no whole number of years: its viewer is held to the strictest cap, 0 (said once)",
				"claim", string(raw))
		})
		age = 0
	}
	return WithMaxRating(ctx, age)
}

// WholeYears reads a JSON value as a whole number of years: a number without
// a fraction, 0 or more. A string, a fraction, a negative number, null and
// anything else are not.
func WholeYears(raw json.RawMessage) (int, bool) {
	var f float64
	if len(raw) == 0 || raw[0] == '"' || string(raw) == "null" || json.Unmarshal(raw, &f) != nil {
		return 0, false
	}
	if f < 0 || f > math.MaxInt32 || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}
