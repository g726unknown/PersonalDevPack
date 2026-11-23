package main

import "testing"

func TestStateDir(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name: "PDP_HOME 优先于一切",
			env:  map[string]string{"PDP_HOME": "/custom/pdp", "XDG_DATA_HOME": "/xdg", "HOME": "/home/u"},
			want: "/custom/pdp",
		},
		{
			name: "XDG_DATA_HOME 次之",
			env:  map[string]string{"XDG_DATA_HOME": "/xdg", "HOME": "/home/u"},
			want: "/xdg/pdp",
		},
		{
			name: "HOME 回退",
			env:  map[string]string{"HOME": "/home/u"},
			want: "/home/u/.local/share/pdp",
		},
		{
			name:    "三者皆无 → error",
			env:     map[string]string{},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := stateDir(func(k string) string { return c.env[k] })
			if c.wantErr {
				if err == nil {
					t.Fatalf("stateDir() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("stateDir() error = %v, want nil", err)
			}
			if got != c.want {
				t.Errorf("stateDir() = %q, want %q", got, c.want)
			}
		})
	}
}
