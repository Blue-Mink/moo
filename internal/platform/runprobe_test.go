package platform

import "testing"

func mkContainer(names []string, state string, mounts []string, wd string) dockerContainer {
	c := dockerContainer{Names: names, State: state, Labels: map[string]string{}}
	for _, m := range mounts {
		c.Mounts = append(c.Mounts, struct {
			Source string `json:"Source"`
		}{Source: m})
	}
	if wd != "" {
		c.Labels["com.docker.compose.project.working_dir"] = wd
	}
	return c
}

// 0.6.348：容器名/挂载/compose 工作目录三种证据都要能认出应用。
func TestContainerMatchesApp(t *testing.T) {
	cases := []struct {
		name      string
		container dockerContainer
		app       string
		want      bool
	}{
		{"容器名精确", mkContainer([]string{"/sub-store"}, "exited", nil, ""), "sub-store", true},
		{"容器名带横杠后缀", mkContainer([]string{"/kerkerker-app"}, "running", nil, ""), "kerkerker", true},
		{"容器名带下划线后缀", mkContainer([]string{"/moo_app-1"}, "running", nil, ""), "moo", true},
		{"容器名大小写不敏感", mkContainer([]string{"/Magic-Qinglong"}, "running", nil, ""), "magic-qinglong", true},
		{"挂载 appdata 路径", mkContainer([]string{"/whatever"}, "running", []string{"/vol1/@appdata/sub-store/data"}, ""), "sub-store", true},
		{"挂载 appcenter 路径", mkContainer([]string{"/x-y"}, "running", []string{"/vol1/@appcenter/kerkerker/docker/config"}, ""), "kerkerker", true},
		{"compose 工作目录", mkContainer([]string{"/abc"}, "running", nil, "/vol1/@appcenter/magic-qinglong/docker"), "magic-qinglong", true},
		{"名字只是前缀相同不应误配", mkContainer([]string{"/miguvideo"}, "running", nil, ""), "mgvideo", false},
		{"无任何证据", mkContainer([]string{"/unrelated"}, "running", []string{"/vol1/@appdata/unrelated/data"}, ""), "sub-store", false},
		{"空应用名", mkContainer([]string{"/sub-store"}, "running", nil, ""), "", false},
	}
	for _, c := range cases {
		if got := containerMatchesApp(c.container, c.app); got != c.want {
			t.Errorf("%s: containerMatchesApp(%v,%q) = %v, want %v", c.name, c.container.Names, c.app, got, c.want)
		}
	}
}

// 0.6.348：进程命令行按应用目录匹配，必须区分「同前缀的不同应用」。
func TestPathMentionsApp(t *testing.T) {
	cases := []struct {
		text string
		app  string
		want bool
	}{
		{"/vol1/@appcenter/fndepot/server/fndepot-amd64 --data-dir=/vol1/@appdata/fndepot", "fndepot", true},
		{"/vol1/@appcenter/moo/moo-server", "moo", true},
		{"/var/apps/trim.preview/trim.preview", "trim.preview", true},
		{"/vol1/@appcenter/moo-325/moo-server", "moo", false}, // 不同目录，不应匹配
		{"/vol1/@appcenter/mooother/bin", "moo", false},
		{"/usr/bin/nginx", "nginx", false},
		{"", "moo", false},
		{"/vol1/@appcenter/moo", "moo", true}, // 路径以应用目录结尾
	}
	for _, c := range cases {
		if got := pathMentionsApp(c.text, c.app); got != c.want {
			t.Errorf("pathMentionsApp(%q,%q) = %v, want %v", c.text, c.app, got, c.want)
		}
	}
}

// 0.6.348：只对 daemon 报 start 的应用做校正，其他状态一律不动。
func TestApplyRealRunStatesSkipsNonStart(t *testing.T) {
	apps := []InstalledApp{
		{AppName: "a", Status: "running"},
		{AppName: "b", Status: "stopped"},
		{AppName: "c", Status: "nostart"},
	}
	applyRealRunStates(nil, apps) // 没有 start 候选 → 不触发探测
	for _, a := range apps {
		switch a.AppName {
		case "a":
			if a.Status != "running" {
				t.Errorf("running 被改写为 %s", a.Status)
			}
		case "b":
			if a.Status != "stopped" {
				t.Errorf("stopped 被改写为 %s", a.Status)
			}
		case "c":
			if a.Status != "nostart" {
				t.Errorf("nostart 被改写为 %s", a.Status)
			}
		}
	}
}
