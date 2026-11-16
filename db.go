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
	res, err := sdk.Exec(sql, args...)
	if err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}
