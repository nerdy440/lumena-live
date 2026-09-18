// Package money enforces integer-only arithmetic on coin and diamond amounts.
// No floating-point type touches this package.
// All amounts are in minor units (coins or diamonds as int64).
package money

import "fmt"

// Currency is the denomination of an amount.
type Currency string

const (
	Coin    Currency = "COIN"
	Diamond Currency = "DIAMOND"
)

// Amount is an integer value in a given currency.
// Use this type for all money in the domain; never use float64.
type Amount struct {
	Value    int64
	Currency Currency
}

func Coins(v int64) Amount    { return Amount{Value: v, Currency: Coin} }
func Diamonds(v int64) Amount { return Amount{Value: v, Currency: Diamond} }

func (a Amount) Add(b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, fmt.Errorf("currency mismatch: %s != %s", a.Currency, b.Currency)
	}
	return Amount{Value: a.Value + b.Value, Currency: a.Currency}, nil
}

func (a Amount) Sub(b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, fmt.Errorf("currency mismatch: %s != %s", a.Currency, b.Currency)
	}
	return Amount{Value: a.Value - b.Value, Currency: a.Currency}, nil
}

func (a Amount) IsNegative() bool { return a.Value < 0 }
func (a Amount) IsZero() bool     { return a.Value == 0 }

// ApplyPlatformTake returns (creator_share, platform_share) given a take rate in basis points (0–10000).
// Uses integer arithmetic exclusively — no rounding error, no float.
// Example: 500 coins, 3000 bps (30%) → creator=350, platform=150.
func ApplyPlatformTake(total Amount, takeBasisPoints int64) (creatorShare, platformShare Amount, err error) {
	if takeBasisPoints < 0 || takeBasisPoints > 10000 {
		return Amount{}, Amount{}, fmt.Errorf("take basis points out of range: %d", takeBasisPoints)
	}
	platformVal := (total.Value * takeBasisPoints) / 10000
	creatorVal := total.Value - platformVal
	return Amount{Value: creatorVal, Currency: total.Currency},
		Amount{Value: platformVal, Currency: total.Currency},
		nil
}

// CoinsToDiamonds converts coins to diamonds using an integer rate.
// Rate is expressed as diamonds per 1000 coins (avoids fractions).
// Example: 500 coins, rate=700 → 500*700/1000 = 350 diamonds.
func CoinsToDiamonds(coins Amount, diamondsPerThousandCoins int64) (Amount, error) {
	if coins.Currency != Coin {
		return Amount{}, fmt.Errorf("expected COIN, got %s", coins.Currency)
	}
	diamonds := (coins.Value * diamondsPerThousandCoins) / 1000
	return Diamonds(diamonds), nil
}
