/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package opentofu

import (
	"os/exec"
	"testing"

	"github.com/MakeNowJust/heredoc"
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

func TestClassify(t *testing.T) {
	tferrs := make(map[string]error)
	expectedOutput := make(map[string]error)

	tferrs["unexpectedName"] = &exec.ExitError{
		Stderr: []byte(heredoc.Doc(`
	│ Error: Unsupported argument
	│
	│   on test.tf line 10, in resource "aws_s3_bucket" "example":
	│   10:   name = "cp-example-${opentofu.workspace}-${random_id.example.hex}"
	│
	│ An argument named "name" is not expected here.
	`)),
	}

	expectedOutput["unexpectedName"] = errors.New(
		heredoc.Doc(
			`OpenTofu encountered an error. Summary: Unsupported argument. To see the full error run: echo "H4sIAAAAAAAA/zyPMWoDMRBFe5/iI1xmhU26hRQpcoTUi6L9jhdbM2Ik4QXjxmfwCX2SsGFxMUzzeLz/fNzxZabW41tKy1mtckSw35YodfN83JcDoILKUn094DwJsd+9YRIYizaLhAuXMpT34afFE6uD4xxSPtP1q2C/6wFISMQHXMzdCnTbq2ZK1UPzF7VTySHy1m2vFmTUNEyjX0l/5Hxzr6ZPeXX+a0e45TlMBaIVnDPjsuZIo9/8AQAA//8BAAD//1Bzr8vrAAAA" | base64 -d | gunzip`,
		),
	)

	tferrs["tooManyListItems"] = &exec.ExitError{
		Stderr: []byte(heredoc.Doc(`
	│ Error: Too many list items
	│
	│   with aws_cognito_user_pool_client.client,
	│   on main.tf line 21, in resource "aws_cognito_user_pool_client" "client":
	│   21:   allowed_oauth_flows = jsondecode(base64decode("ICBbIkFMTE9XX0FETUlOX1VTRVJfUEFTU1dPUkRfQVVUSCIsICJBTExPV19SRUZSRVNIX1RPS0VOX0FVVEgiLCAiQUxMT1dfVVNFUl9QQVNTV09SRF9BVVRIIiwgIkFMTE9XX1VTRVJfU1JQX0FVVEgiXQo="))
	│
	│ Attribute allowed_oauth_flows supports 3 item maximum, but config has 4 declared.
	`)),
	}

	expectedOutput["tooManyListItems"] = errors.New(
		heredoc.Doc(
			`OpenTofu encountered an error. Summary: Too many list items. To see the full error run: echo "H4sIAAAAAAAA/3yRwWrbQBBA7/mKQacGjNGmoSBDDrGQQKZxLGl3Eb2IlbSSt1ntmN0Vcq/5hnyhv6S0lX0qOQwzA8N7zMzl4x0Sa9FugCLCKMwv0Mp5UF6O7u7y8f4nAGBW/ghidnWLg1Ee68lJW58Qdd1qJY1f/0urZR4NjEKZte9BKyPhgaxAGbDS4WRbCcFnrACCpdgsuAeyAQChNc6yq1FM/lj3GmcHT/DToelki5380ggnvz0uTZDF2yZ7S19oElVVmCaU6deKcFrwXc+SlDLSHdhb0eecszLOXBbvtjQ5HziJyoL9KAu+zypSHMqQv1ZhynkyqO/xs8rZ+YWSrud8nzId5TnfUx5GZZFGW86LLFPzcPNefWSXXxlVjk/B/f3tus/eW9VMXv53QTedTmi9g69/nwKjOKtxGlfQTB5aNL0a4CgcPEInWy2s7NZ3vwEAAP//AQAA//9AvYb+1wEAAA==" | base64 -d | gunzip`,
		),
	)

	output := Classify(tferrs["unexpectedName"])

	if output.Error() != expectedOutput["unexpectedName"].Error() {
		t.Errorf("Unexpected error classification got:\n`%s`\nexpected:\n`%s`", output, expectedOutput["unexpectedName"])
	}

	output = Classify(tferrs["tooManyListItems"])

	if output.Error() != expectedOutput["tooManyListItems"].Error() {
		t.Errorf("Unexpected error classification got:\n`%s`\nexpected:\n`%s`", output, expectedOutput["tooManyListItems"])
	}
}

func TestFormatTofuErrorOutput(t *testing.T) {
	tofuerrs := make(map[string]string)
	expectedOutput := make(map[string]map[string]string)

	tofuerrs["unexpectedName"] = heredoc.Doc(`
	│ Error: Unsupported argument
	│
	│   on test.tf line 10, in resource "aws_s3_bucket" "example":
	│   10:   name = "cp-example-${opentofu.workspace}-${random_id.example.hex}"
	│
	│ An argument named "name" is not expected here.
	`)

	expectedOutput["unexpectedName"] = make(map[string]string)
	expectedOutput["unexpectedName"]["summary"] = heredoc.Doc(`
	Unsupported argument`)

	expectedOutput["unexpectedName"]["base64full"] = "H4sIAAAAAAAA/zyPMWoDMRBFe5/iI1xmhU26hRQpcoTUi6L9jhdbM2Ik4QXjxmfwCX2SsGFxMUzzeLz/fNzxZabW41tKy1mtckSw35YodfN83JcDoILKUn094DwJsd+9YRIYizaLhAuXMpT34afFE6uD4xxSPtP1q2C/6wFISMQHXMzdCnTbq2ZK1UPzF7VTySHy1m2vFmTUNEyjX0l/5Hxzr6ZPeXX+a0e45TlMBaIVnDPjsuZIo9/8AQAA//8BAAD//1Bzr8vrAAAA"

	tofuerrs["tooManyListItems"] = heredoc.Doc(`
	│ Error: Too many list items
	│
	│   with aws_cognito_user_pool_client.client,
	│   on main.tf line 21, in resource "aws_cognito_user_pool_client" "client":
	│   21:   allowed_oauth_flows = jsondecode(base64decode("ICBbIkFMTE9XX0FETUlOX1VTRVJfUEFTU1dPUkRfQVVUSCIsICJBTExPV19SRUZSRVNIX1RPS0VOX0FVVEgiLCAiQUxMT1dfVVNFUl9QQVNTV09SRF9BVVRIIiwgIkFMTE9XX1VTRVJfU1JQX0FVVEgiXQo="))
	│
	│ Attribute allowed_oauth_flows supports 3 item maximum, but config has 4 declared.
	`)

	expectedOutput["tooManyListItems"] = make(map[string]string)
	expectedOutput["tooManyListItems"]["summary"] = heredoc.Doc(`
	Too many list items`)

	expectedOutput["tooManyListItems"]["base64full"] = "H4sIAAAAAAAA/3yRwWrbQBBA7/mKQacGjNGmoSBDDrGQQKZxLGl3Eb2IlbSSt1ntmN0Vcq/5hnyhv6S0lX0qOQwzA8N7zMzl4x0Sa9FugCLCKMwv0Mp5UF6O7u7y8f4nAGBW/ghidnWLg1Ee68lJW58Qdd1qJY1f/0urZR4NjEKZte9BKyPhgaxAGbDS4WRbCcFnrACCpdgsuAeyAQChNc6yq1FM/lj3GmcHT/DToelki5380ggnvz0uTZDF2yZ7S19oElVVmCaU6deKcFrwXc+SlDLSHdhb0eecszLOXBbvtjQ5HziJyoL9KAu+zypSHMqQv1ZhynkyqO/xs8rZ+YWSrud8nzId5TnfUx5GZZFGW86LLFPzcPNefWSXXxlVjk/B/f3tus/eW9VMXv53QTedTmi9g69/nwKjOKtxGlfQTB5aNL0a4CgcPEInWy2s7NZ3vwEAAP//AQAA//9AvYb+1wEAAA=="

	summary, base64FullErr, err := formatTofuErrorOutput(tofuerrs["unexpectedName"])
	if err != nil {
		t.Errorf("Unexpected error: %s", err)
	}

	if summary != expectedOutput["unexpectedName"]["summary"] {
		t.Errorf(
			"Unexpected error summary value got:`%s`\nexpected: `%s`",
			summary,
			expectedOutput["unexpectedName"]["summary"],
		)
	}

	if base64FullErr != expectedOutput["unexpectedName"]["base64full"] {
		t.Errorf(
			"Unexpected error base64full got:`%s`\nexpected: `%s`",
			base64FullErr,
			expectedOutput["unexpectedName"]["base64full"],
		)
	}

	summary, base64FullErr, err = formatTofuErrorOutput(tofuerrs["tooManyListItems"])

	if err != nil {
		t.Errorf("Unexpected error: %s", err)
	}

	if summary != expectedOutput["tooManyListItems"]["summary"] {
		t.Errorf(
			"Unexpected error classification got:`%s`\nexpected: `%s`",
			summary,
			expectedOutput["tooManyListItems"]["summary"],
		)
	}

	if base64FullErr != expectedOutput["tooManyListItems"]["base64full"] {
		t.Errorf(
			"Unexpected error base64full got:`%s`\nexpected: `%s`",
			base64FullErr,
			expectedOutput["tooManyListItems"]["base64full"],
		)
	}
}
