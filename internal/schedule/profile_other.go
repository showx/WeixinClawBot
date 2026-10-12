//go:build !windows

package schedule

type savedLogin struct {
	exe      string
	userData string
	profile  string
}

func findLogin(string) (savedLogin, error) { return savedLogin{}, nil }

func seedLogin(string, savedLogin) error { return nil }
