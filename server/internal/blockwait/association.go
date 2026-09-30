package blockwait

import (
	"fmt"
	"strings"
)

// RejectAssociationWait refuses a wait that asks the platform to notice a
// pull request on its own. The close that has the link uses --pr; a wait
// never produces that link (DENE-961).
func RejectAssociationWait(in Input, rec Record) error {
	if isAssociationWait(in.WaitCondition) || isAssociationWait(in.WaitProbe) ||
		isAssociationWait(rec.WaitCondition) || isAssociationWait(rec.WaitProbe) {
		return fmt.Errorf("不能用「等平台关联 PR」卡住。改用 `multica issue close <票号> --pr <链接>`，平台会核实；核不到也放行并标成未核实")
	}
	return nil
}

func isAssociationWait(text string) bool {
	s := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(text)), " "))
	if s == "" {
		return false
	}
	for _, phrase := range []string{
		"等平台关联",
		"等待平台关联",
		"平台关联 pr",
		"等待关联 pr",
		"等关联 pr",
		"等 pr 关联",
		"等待 pr 关联",
		"wait for the platform to link",
		"waiting for the platform to link",
		"wait for the platform to associate",
		"waiting for the platform to associate",
	} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return strings.Contains(s, "关联") && strings.Contains(s, "平台")
}
