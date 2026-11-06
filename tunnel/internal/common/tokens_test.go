package common

import (
	"context"
	"errors"
	stdhttp "net/http"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
)

func TestTokenStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{linktoken.ErrNotFound, stdhttp.StatusNotFound},
		{&config.Error{Msg: "x"}, stdhttp.StatusBadRequest},
		{config.Errors{&config.Error{}}, stdhttp.StatusBadRequest},
		{app.ErrBusy, stdhttp.StatusServiceUnavailable},
		{context.DeadlineExceeded, stdhttp.StatusServiceUnavailable},
		{errors.New("x"), stdhttp.StatusInternalServerError},
	} {
		if got := TokenStatus(tc.err); got != tc.want {
			t.Errorf("%v: %d, want %d", tc.err, got, tc.want)
		}
	}
}
