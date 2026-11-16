package main

import (
	"errors"

	"github.com/datahearth/streamline/extensions/sdk"
)

// query and exec fold the SDK's two failure channels, a failed host call and a
// statement the database rejected, into one error.
func query(sql string, args ...any) (sdk.SQLResult, error) {
	res, err := sdk.Query(sql, args...)
	if err != nil {
		return sdk.SQLResult{}, err
	}
	if res.Error != "" {
		return sdk.SQLResult{}, errors.New(res.Error)
	}
	return res, nil
}

func exec(sql string, args ...any) error {
	_, err := run(sql, args...)
	return err
}

// run is exec that also returns the result, for LastInsertID.
func run(sql string, args ...any) (sdk.SQLResult, error) {
	res, err := sdk.Exec(sql, args...)
	if err != nil {
		return sdk.SQLResult{}, err
	}
	if res.Error != "" {
		return sdk.SQLResult{}, errors.New(res.Error)
	}
	return res, nil
}

type row []any

func (r row) str(i int) string {
	s, _ := r[i].(string)
	return s
}

func (r row) int(i int) int64 {
	f, _ := r[i].(float64)
	return int64(f)
}
