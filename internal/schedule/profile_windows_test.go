//go:build windows

package schedule

import "testing"

func TestHostNeedlesSkipIPParent(t *testing.T) {
	got := hostNeedles("http://127.0.0.1:9/report")
	if len(got) != 1 || got[0] != "127.0.0.1" {
		t.Fatalf("IP 不应拆出父域: %v", got)
	}
	got = hostNeedles("https://mgadminv4.imoguang.com/adm2021/putin/ads-report")
	if len(got) != 2 || got[0] != "mgadminv4.imoguang.com" || got[1] != "imoguang.com" {
		t.Fatalf("域名父域不对: %v", got)
	}
}
