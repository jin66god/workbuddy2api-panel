package main

import (
	"reflect"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/loginsite"
)

// TestParseSiteArgs --site 三站点、--realm 兼容映射、非法值报错。
func TestParseSiteArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantID   string
		wantAPI  string
		wantRest []string
		wantErr  bool
	}{
		{"intl", []string{"--site=intl", "url"}, loginsite.SiteIntl, loginsite.BaseOfficial, []string{"url"}, false},
		{"workbuddy", []string{"--site", "workbuddy", "poll"}, loginsite.SiteWorkBuddy, loginsite.BaseWorkBuddy, []string{"poll"}, false},
		{"cn-default", []string{"url"}, loginsite.SiteCN, "https://copilot.tencent.com", []string{"url"}, false},
		// 老脚本只传 realm=global → 旧桌面端（语义不变）。
		{"realm-global", []string{"--realm=global", "url"}, loginsite.SiteWorkBuddy, loginsite.BaseWorkBuddy, []string{"url"}, false},
		// --site 显式优先，同时消化 --realm。
		{"site-wins", []string{"--realm=global", "--site=intl", "url"}, loginsite.SiteIntl, loginsite.BaseOfficial, []string{"url"}, false},
		{"invalid", []string{"--site=tokyo", "url"}, "", "", nil, true},
		{"missing", []string{"--site"}, "", "", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			site, rest, err := parseSiteArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got site=%+v rest=%v", site, rest)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if site.ID != c.wantID || site.API != c.wantAPI {
				t.Errorf("site=%+v want id=%s api=%s", site, c.wantID, c.wantAPI)
			}
			if !reflect.DeepEqual(rest, c.wantRest) {
				t.Errorf("rest=%v want %v", rest, c.wantRest)
			}
		})
	}
}

// TestSiteRealmConsistency intl 与 workbuddy 同属 global realm，但主机必须不同。
func TestSiteRealmConsistency(t *testing.T) {
	intl := loginsite.For(loginsite.SiteIntl)
	wb := loginsite.For(loginsite.SiteWorkBuddy)
	if intl.Realm != "global" || wb.Realm != "global" {
		t.Errorf("intl/workbuddy realm: %s/%s want global", intl.Realm, wb.Realm)
	}
	if intl.API == wb.API {
		t.Errorf("intl 与 workbuddy 的 API 不应相同: %s", intl.API)
	}
}
