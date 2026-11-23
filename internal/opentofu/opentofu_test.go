/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package opentofu

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/MakeNowJust/heredoc/v2"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
)

func TestOutputStringValue(t *testing.T) {
	cases := map[string]struct {
		o    Output
		want string
	}{
		"ValueIsString": {
			o:    Output{value: "imastring!"},
			want: "imastring!",
		},
		"ValueIsNotString": {
			o:    Output{value: 42},
			want: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.o.StringValue()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\no.StringValue(): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestOutputNumberValue(t *testing.T) {
	cases := map[string]struct {
		o    Output
		want float64
	}{
		"ValueIsFloat": {
			o:    Output{value: float64(42.0)},
			want: 42.0,
		},
		// We create outputs by decoding from JSON, so numbers should always be
		// a float64.
		"ValueIsNotFloat": {
			o:    Output{value: 42},
			want: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.o.NumberValue()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\no.NumberValue(): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestOutputBoolValue(t *testing.T) {
	cases := map[string]struct {
		o    Output
		want bool
	}{
		"ValueIsBool": {
			o:    Output{value: true},
			want: true,
		},
		"ValueIsNotBool": {
			o:    Output{value: "DEFINITELY!"},
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.o.BoolValue()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\no.BoolValue(): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestOutputJSONValue(t *testing.T) {
	type want struct {
		j   []byte
		err error
	}
	cases := map[string]struct {
		o    Output
		want want
	}{
		"ValueIsString": {
			o: Output{value: "imastring!"},
			want: want{
				j: []byte(`"imastring!"`),
			},
		},
		"ValueIsNumber": {
			o: Output{value: 42.0},
			want: want{
				j: []byte(`42`),
			},
		},
		"ValueIsBool": {
			o: Output{value: true},
			want: want{
				j: []byte(`true`),
			},
		},
		"ValueIsTuple": {
			o: Output{value: []any{"imastring", 42, true}},
			want: want{
				j: []byte(`["imastring",42,true]`),
			},
		},
		"ValueIsObject": {
			o: Output{value: map[string]any{
				"cool": 42,
			}},
			want: want{
				j: []byte(`{"cool":42}`),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := tc.o.JSONValue()
			if diff := cmp.Diff(tc.want.err, err); diff != "" {
				t.Errorf("\no.JSONValue(): -want error, +got error:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.j, got); diff != "" {
				t.Errorf("\no.JSONValue(): -want, +got:\n%s", diff)
			}
		})
	}
}

var stderrUnsupportedArgument = heredoc.Doc(`
	│ Error: Unsupported argument
	│
	│   on test.tf line 10, in resource "aws_s3_bucket" "example":
	│   10:   name = "cp-example-${opentofu.workspace}-${random_id.example.hex}"
	│
	│ An argument named "name" is not expected here.
	`)

var stderrTooManyListItems = heredoc.Doc(`
	│ Error: Too many list items
	│
	│   with aws_cognito_user_pool_client.client,
	│   on main.tf line 21, in resource "aws_cognito_user_pool_client" "client":
	│   21:   allowed_oauth_flows = jsondecode(base64decode("ICBbIkFMTE9XX0FETUlOX1VTRVJfUEFTU1dPUkRfQVVUSCIsICJBTExPV19SRUZSRVNIX1RPS0VOX0FVVEgiLCAiQUxMT1dfVVNFUl9QQVNTV09SRF9BVVRIIiwgIkFMTE9XX1VTRVJfU1JQX0FVVEgiXQo="))
	│
	│ Attribute allowed_oauth_flows supports 3 item maximum, but config has 4 declared.
	`)

// gunzipBase64 reverses the encoding of formatTofuErrorOutput. Tests compare
// the decoded text because compress/gzip output differs between Go releases.
func gunzipBase64(t *testing.T, encoded string) string {
	t.Helper()

	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64.StdEncoding.DecodeString(%q): %v", encoded, err)
	}

	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip.NewReader(...): %v", err)
	}

	decoded, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll(...): %v", err)
	}

	return string(decoded)
}

func TestClassify(t *testing.T) {
	classified := regexp.MustCompile(`^OpenTofu encountered an error\. Summary: (.*)\. To see the full error run: echo "([A-Za-z0-9+/=]*)" \| base64 -d \| gunzip$`)

	type args struct {
		err error
	}
	type want struct {
		summary string
		output  string
	}

	cases := map[string]struct {
		args args
		want want
	}{
		"UnsupportedArgument": {
			args: args{err: &exec.ExitError{Stderr: []byte(stderrUnsupportedArgument)}},
			want: want{summary: "Unsupported argument", output: stderrUnsupportedArgument},
		},
		"TooManyListItems": {
			args: args{err: &exec.ExitError{Stderr: []byte(stderrTooManyListItems)}},
			want: want{summary: "Too many list items", output: stderrTooManyListItems},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := Classify(tc.args.err)

			m := classified.FindStringSubmatch(err.Error())
			if m == nil {
				t.Fatalf("Classify(...): %q does not match %s", err, classified)
			}

			got := want{summary: m[1], output: gunzipBase64(t, m[2])}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\nClassify(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestFormatTofuErrorOutput(t *testing.T) {
	type args struct {
		errorOutput string
	}
	type want struct {
		summary string
		output  string
	}

	cases := map[string]struct {
		args args
		want want
	}{
		"UnsupportedArgument": {
			args: args{errorOutput: stderrUnsupportedArgument},
			want: want{summary: "Unsupported argument", output: stderrUnsupportedArgument},
		},
		"TooManyListItems": {
			args: args{errorOutput: stderrTooManyListItems},
			want: want{summary: "Too many list items", output: stderrTooManyListItems},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			summary, base64FullErr, err := formatTofuErrorOutput(tc.args.errorOutput)
			if err != nil {
				t.Fatalf("formatTofuErrorOutput(...): %v", err)
			}

			got := want{summary: summary, output: gunzipBase64(t, base64FullErr)}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("\nformatTofuErrorOutput(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	t.Run("ContextDoneBeforeStart", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := runCommand(ctx, exec.CommandContext(ctx, "sleep", "30"))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("runCommand(...): want context.Canceled, got %v", err)
		}
	})
	t.Run("ContextDoneWhileRunning", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := runCommand(ctx, exec.CommandContext(ctx, "sleep", "30"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("runCommand(...): want context.DeadlineExceeded, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("runCommand(...) took %s, the process was not terminated on cancellation", elapsed)
		}
	})
	t.Run("ContextDoneAfterSuccess", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		// sh exits at once, but the background sleep keeps stdout open, so
		// runCommand returns only after the context has ended.
		out, err := runCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "echo ok; sleep 1 &"))
		if ctx.Err() == nil {
			t.Fatal("runCommand(...) returned before the context ended")
		}
		if err != nil {
			t.Fatalf("runCommand(...): unexpected error %v", err)
		}
		if string(out) != "ok\n" {
			t.Errorf("runCommand(...): want %q, got %q", "ok\n", string(out))
		}
	})
	t.Run("Succeeds", func(t *testing.T) {
		ctx := context.Background()
		out, err := runCommand(ctx, exec.CommandContext(ctx, "echo", "ok"))
		if err != nil {
			t.Fatalf("runCommand(...): unexpected error %v", err)
		}
		if string(out) != "ok\n" {
			t.Errorf("runCommand(...): want %q, got %q", "ok\n", string(out))
		}
	})
}
