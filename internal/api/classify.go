package api

import (
	"regexp"
	"strings"
)

// 智能自动归类 v4（2026-09-26 第二轮全量审计）：
// 逐应用审计官方 375 + conversun 130 + fndepot 长尾 585，精选表扩到 667 条；
// 官方「实用效率」等粗粒度 catch-all 按领域细化（内网穿透/组网→网络工具、
// 视频下载器→下载、直播录制/媒体库自动化→媒体自动化、游戏串流→游戏等）；
// AMD 驱动族（rocm/ai-runtime/ntfs3/aic8800/broadcom-sta）统一归驱动。
//
// 智能自动归类 v3：以飞牛官方应用中心的分类体系为准（2026-09 调研）。
//
// 官方分类体系（面板 /app-center/v1/app/list 全量 375 应用 tags 实锤，
// 一应用可多标签）：实用效率 / 开发工具 / 生活服务 / 影音娱乐 / 游戏 /
// AI / 备份同步 / 摄影摄像 / 驱动(Drive) / 下载。
// 官方口径要点：网盘工具（OpenList/alist/baidu.netdisk）归备份同步；
// frpc/frps/监控/容器/堡垒机归开发工具；*arr 归影音娱乐；照片/NVR 归
// 摄影摄像；阅读/记账/智能家居归生活服务；AdGuardHome/DDNS-GO 等归
// 实用效率。
// fndepot 生态（conversun/fnos-apps 165 应用人工归类）补充官方未覆盖
// 的域：网络工具（tailscale/frp/mihomo/远程）、媒体自动化（*arr 套件+
// 字幕）、浏览器——三者按「官方类别不够则新增」原则加入，共 13 类。
// 分类哲学与开源社区一致（freedesktop XDG / F-Droid / Google Play）：
// 按主用途领域归类，技术特征（AI 驱动/自动化实现）不作主依据。
//
// 处理顺序（classifyAll，目录组装末尾统一后处理）：
//  1. 精选表 curatedCategories（官方 372 + conversun 128，官方优先，
//     跨源同名应用统一归类）→ 直接采用；
//  2. 源标签 labelCategoryMap（官方 tags / fndepot 中文标签 → 13 键）；
//  3. 关键词规则（领域特征词；通用词只匹配名称，简介顺带提及不算）。

var validCategoryKeys = map[string]bool{
	"ai": true, "media": true, "automation": true, "game": true,
	"photo": true, "efficiency": true, "devtools": true, "lifestyle": true,
	"backup": true, "download": true, "network": true, "browser": true,
	"driver": true,
}

// curatedCategories 精选归类表：官方应用中心 372 应用（tags 多标签按
// 领域特异性取主，*arr→automation、网络连通→network、驱动→driver 细化）
// + conversun/fnos-apps 人工归类 128 应用（重映射到官方领域口径）。
var curatedCategories = map[string]string{
	// ai (52)
	"9router": "ai", "ai-draw-io": "ai", "ai_installer": "ai",
	"antigravity": "ai", "app.native.lobechat": "ai", "astrbot": "ai",
	"burncloud": "ai", "chat2api": "ai", "com.dustinky.qwenpaw": "ai",
	"copaw": "ai", "cowagent": "ai", "deepseek.harness": "ai",
	"deeptutor": "ai", "docker-lobechat": "ai", "easyvoice": "ai",
	"ekko": "ai", "fastflowlm": "ai", "fn-deepseek-harness": "ai",
	"fpk-oneapi": "ai", "genoffice": "ai", "harness": "ai",
	"hermes": "ai", "hermesagent": "ai", "hermesstudio": "ai",
	"hyatlas": "ai", "librechat": "ai", "lobechat": "ai",
	"localai": "ai", "loomyproxy": "ai", "maxkb": "ai",
	"mtranserver": "ai", "nanobot": "ai", "new-api": "ai",
	"next-ai-draw-io": "ai", "oc-deploy": "ai", "octopus": "ai",
	"ollama": "ai", "open-webui": "ai", "openclaw-doc": "ai",
	"openvino-ovms": "ai", "picoclaw": "ai", "qoder2api": "ai",
	"qwenpaw": "ai", "qwenpaw_yuexps": "ai", "sag": "ai",
	"trim.hermes": "ai", "trim.openclaw": "ai", "waoo": "ai",
	"wb2api": "ai", "xx-openclaw": "ai", "xxclaw": "ai",
	"zeroclaw": "ai",
	// automation (28)
	"ani-rss": "automation", "auto-bangumi": "automation", "bazarr": "automation",
	"bililive-go": "automation", "bililivetools": "automation", "chinesesubfinder": "automation",
	"docker-autobangumi": "automation", "dy.net": "automation", "jackett": "automation",
	"jellystat": "automation", "lidarr": "automation", "magic-bilisync": "automation",
	"medusa": "automation", "metatube": "automation", "mkv-subtitle-translator-fnos": "automation",
	"moviepilot": "automation", "moviepilot-v2": "automation", "prowlarr": "automation",
	"qmediasync": "automation", "radarr": "automation", "readarr": "automation",
	"smartstrm": "automation", "sonarr": "automation", "streamcap": "automation",
	"suggestarr": "automation", "t3fap-next": "automation", "tautulli": "automation",
	"tinymediamanager": "automation",
	// browser (7)
	"chrome-for-testing-yuexps": "browser", "chromium": "browser", "firefox": "browser",
	"firefox-docker": "browser", "fn-chromium": "browser", "fn-chromium-desktop": "browser",
	"fygo-browser": "browser",
	// game (58)
	"beking": "game", "bond": "game", "boombird": "game",
	"clawstrike": "game", "com.tencent.tgpa.turborxnas": "game", "cs16server": "game",
	"cuber": "game", "cutcubes": "game", "cyber-zen": "game",
	"docker-gsmanager": "game", "docker-palworld": "game", "dzk3d": "game",
	"eaglercraft": "game", "eaglercraftx-1.8.8": "game", "feiniu-sanguo": "game",
	"fncube": "game", "fndesk_jigsaw": "game", "fnos-aircraft": "game",
	"fnos-app-schulte": "game", "fnos.sudoku": "game", "fpk-factorio": "game",
	"frontend-killer": "game", "fruit": "game", "game-space": "game",
	"goldfishies": "game", "gridmerge": "game", "guandan": "game",
	"hamsters": "game", "igame": "game", "jilehe": "game",
	"landlord": "game", "liars-bar": "game", "liferestart": "game",
	"lite.game": "game", "mahjong": "game", "majiang": "game",
	"mcsmanager": "game", "minesweeper": "game", "moonlight-web": "game",
	"mslx": "game", "panda-xiangqi": "game", "playgl": "game",
	"pvz-portable": "game", "pvz2-gardendless": "game", "qrubiks": "game",
	"react-tetris-fnos": "game", "space-pinball": "game", "spacecadetpinball": "game",
	"stickfigure": "game", "subwaysurfers": "game", "sunshine": "game",
	"techfunway-brick-game": "game", "templerun2": "game", "tombrunner": "game",
	"webcs": "game", "webmc": "game", "whereisfnos": "game",
	"xiangqi": "game",
	// photo (18)
	"cloud-idphoto": "photo", "docker-idphotos": "photo", "easynvr": "photo",
	"ente": "photo", "frigate": "photo", "fuji-autosave": "photo",
	"immich": "photo", "lite.gallery": "photo", "niupic": "photo",
	"oldmemories": "photo", "onvif-nvr": "photo", "photomark": "photo",
	"photoprism": "photo", "trim.photos": "photo", "trim.seek": "photo",
	"vibenvr": "photo", "xiaomi-miloco": "photo", "xiaomi-miloco-plus": "photo",
	// media (59)
	"app.native.flynarwhalserver": "media", "audiodock": "media", "autoproxy": "media",
	"com.movicloud.fnnas": "media", "danmu-api": "media", "daoliyu.music": "media",
	"docker-jellyseerr": "media", "docker-komga": "media", "duanju": "media",
	"embyserver": "media", "embyserver4-9": "media", "fn-kodi": "media",
	"fn-videostream": "media", "fnpiano": "media", "handbrake": "media",
	"iptv": "media", "jellyfin": "media", "jellyseerr": "media",
	"juneix.airplay2": "media", "juneix.embyx": "media", "kavita": "media",
	"knas-music-organizer": "media", "koel": "media", "komga": "media",
	"leelaa.playlist": "media", "lite.music": "media", "lite.video": "media",
	"lyranest": "media", "lyranest-xiaoai-bridge": "media", "mediaplayer": "media",
	"mediavault": "media", "melody-hub": "media", "miair-next": "media",
	"micast": "media", "minimal-music": "media", "musebox": "media",
	"music-meta-web": "media", "music-tidy": "media", "music_tag_web": "media",
	"musicfree": "media", "navidrome": "media", "nexplay": "media",
	"ombi": "media", "plex": "media", "plexmediaserver": "media",
	"rtp2httpd": "media", "seerr": "media", "songloft": "media",
	"steam-recorder": "media", "te-losslesscut-nas": "media", "trim.media": "media",
	"trim.music": "media", "tt-music": "media", "unlockmusic": "media",
	"video-converter": "media", "video_transfer": "media", "wizarr": "media",
	"xiaomusic": "media", "yanded.music": "media",
	// network (81)
	"adguardhome": "network", "cf-dns-select": "network", "cftun-ui": "network",
	"clashlite": "network", "clientlink": "network", "cloudflare-qt": "network",
	"cloudflared": "network", "com.dustinky.tunnel": "network", "com.istoreos.vm": "network",
	"cpolar-native": "network", "ddns-go": "network", "ddnsto": "network",
	"dns-health": "network", "docker-beyondnetwork": "network", "easytier": "network",
	"easytier-eui": "network", "easytier-eui.user": "network", "easytier-web": "network",
	"fakehttp": "network", "fakesip": "network", "feishunet": "network",
	"fluxor": "network", "fn-guacamole": "network", "fn-knock-docker": "network",
	"fn-knock-lite": "network", "fn-wireguard": "network", "fn-zerotier": "network",
	"fpk-ovs-switch-fn": "network", "fpk-rustdesk-server": "network", "frpc": "network",
	"frps": "network", "headscale": "network", "istoreos": "network",
	"kspeeder": "network", "lucky": "network", "lucky.yuexps": "network",
	"magic-ddnsgo": "network", "mefrp": "network", "mihomo": "network",
	"mihomo-arm64": "network", "mihomo-armv7": "network", "mihomov2": "network",
	"mosdns": "network", "msf": "network", "myspeed": "network",
	"naturetunnel": "network", "netbird": "network", "npc": "network",
	"nps": "network", "numa": "network", "one-kvm": "network",
	"openspeedtest": "network", "opensurge": "network", "orbien": "network",
	"owjdxb": "network", "p2pee-proxy": "network", "passnat": "network",
	"pgyvpn": "network", "phddns": "network", "pihole": "network",
	"qwrt": "network", "rustdesk-server": "network", "sakurafrp": "network",
	"scrcpynas": "network", "shenzhuohl": "network", "smartdns": "network",
	"stundeck-fpk": "network", "sub-store": "network", "substore": "network",
	"tailscale": "network", "traffic-keeper": "network", "upsnap": "network",
	"v2raya": "network", "vnt2": "network", "vnts2": "network",
	"webtop": "network", "wg-easy": "network", "wol": "network",
	"wolgoweb": "network", "zeronews": "network", "zerotier": "network",
	// backup (35)
	"alist": "backup", "alist3": "backup", "app.hidav.clouddrive": "backup",
	"baidu.netdisk": "backup", "clouddrive": "backup", "clouddrive2": "backup",
	"cloudreve": "backup", "docker-xiaoya": "backup", "duplicati": "backup",
	"fpk-cloudreve": "backup", "fpk-icloud-photos": "backup", "fpk-panindex": "backup",
	"fpk-zflie": "backup", "icloudphotosdownloader": "backup", "kodbox": "backup",
	"linkease": "backup", "litepan": "backup", "nextcloud": "backup",
	"openlist": "backup", "openlist-beta": "backup", "openlistnative": "backup",
	"rclone": "backup", "resilio-sync": "backup", "seafile": "backup",
	"syncthing": "backup", "taosync": "backup", "tenfellos": "backup",
	"trim.snapshots": "backup", "trim.sync_server": "backup", "usbrsync": "backup",
	"usbrsync1": "backup", "usbrsynccgi": "backup", "verysync": "backup",
	"wxbackup": "backup", "zhijiayingpan": "backup",
	// download (46)
	"aellus": "download", "aria2": "download", "aria2-next": "download",
	"bitcomet": "download", "bitmeteor": "download", "cloud-collection": "download",
	"cloud-resources": "download", "cloudimgs": "download", "cloudlink-finder": "download",
	"cloudsaver": "download", "com.dustinky.ipatool": "download", "copyparty": "download",
	"deen-music-downloader": "download", "docker-iyuuplus": "download", "easydown": "download",
	"f2media": "download", "filecodebox": "download", "fn-seekbox": "download",
	"fn_qycs": "download", "fnm3u8": "download", "fnm3u8dl": "download",
	"fnytdlp": "download", "gopeed": "download", "imageadmin": "download",
	"localsend-web": "download", "metube": "download", "miyin": "download",
	"motrix": "download", "p2pee": "download", "panhub": "download",
	"pansou": "download", "peerbanhelper": "download", "pichub": "download",
	"qbittorrent": "download", "qbittorrent-enhanced": "download", "qbittorrent.nox": "download",
	"qilindrop": "download", "quark-auto-save": "download", "sabnzbd": "download",
	"savextube": "download", "t3mt": "download", "transmission": "download",
	"tunegram": "download", "xunlei": "download", "zdir": "download",
	"zjjx": "download",
	// devtools (111)
	"1panel": "devtools", "all.editor": "devtools", "allinssl": "devtools",
	"app.native.certimate": "devtools", "arcadia": "devtools", "arcane": "devtools",
	"asdf": "devtools", "bark": "devtools", "beszel": "devtools",
	"bunjs": "devtools", "certimate": "devtools", "clamav": "devtools",
	"code.editor": "devtools", "coder-docker": "devtools", "com.dongguaha.vm": "devtools",
	"com.dustinky.it-tools": "devtools", "com.fnos.dashboard": "devtools", "coolercontrol": "devtools",
	"daidai-panel": "devtools", "devpi": "devtools", "docker-baota": "devtools",
	"docker-copilot": "devtools", "docker-reference": "devtools", "docker-uptime-kuma": "devtools",
	"dpanel": "devtools", "erlang": "devtools", "fn-codeserver": "devtools",
	"fn-fancontrol": "devtools", "fn-reverseproxy": "devtools", "fn-scheduler": "devtools",
	"fn-sshd-config": "devtools", "fn-ups-manager": "devtools", "fn-vm-x86": "devtools",
	"fnmessagebot": "devtools", "fnnas.btrfs.restore": "devtools", "fnos-apps-store": "devtools",
	"fnpackup": "devtools", "forgejo": "devtools", "fpk-dockercopilot": "devtools",
	"git": "devtools", "gitea": "devtools", "glances": "devtools",
	"go-1.24": "devtools", "go-1.26": "devtools", "gocron": "devtools",
	"golang": "devtools", "gotify": "devtools", "gotty": "devtools",
	"grafana": "devtools", "grafana.alloy": "devtools", "grafana.loki": "devtools",
	"hanyemonitor": "devtools", "hashfile": "devtools", "hotify-server": "devtools",
	"httpsgate": "devtools", "it-tools": "devtools", "iventoy": "devtools",
	"java-11-openjdk": "devtools", "java-17-openjdk": "devtools", "java-21-openjdk": "devtools",
	"jmglink": "devtools", "komari": "devtools", "komari-agent": "devtools",
	"loki": "devtools", "magicpush": "devtools", "mattermost": "devtools",
	"mcpcat": "devtools", "minio": "devtools", "modernmirror": "devtools",
	"moo": "devtools", "n8n": "devtools", "netdata": "devtools",
	"nginx-proxy-manager": "devtools", "nginx-ui": "devtools", "nginxserver": "devtools",
	"node-red": "devtools", "nodejs_v14": "devtools", "nodejs_v16": "devtools",
	"nodejs_v18": "devtools", "nodejs_v20": "devtools", "nodejs_v22": "devtools",
	"nodejs_v24": "devtools", "ntfy": "devtools", "php-runtime": "devtools",
	"pocket-id": "devtools", "prometheus": "devtools", "prometheus.node_exporter": "devtools",
	"prometheus.prometheus": "devtools", "python310": "devtools", "python311": "devtools",
	"python312": "devtools", "python313": "devtools", "python314": "devtools",
	"python38": "devtools", "python39": "devtools", "qinglong": "devtools",
	"r1toolbox": "devtools", "rabbitmq": "devtools", "redis": "devtools",
	"rocketchat": "devtools", "techfunway-sqlite-manage": "devtools", "trim.iscsi": "devtools",
	"trim.security": "devtools", "trim.vm": "devtools", "ubuntu-xfce": "devtools",
	"uptime-kuma": "devtools", "visor": "devtools", "vs-code": "devtools",
	"watchcow": "devtools", "xiaohu-mcpproxy": "devtools", "xiaohu-supervisor": "devtools",
	// lifestyle (51)
	"actual-budget": "lifestyle", "anniversary-reminder": "lifestyle", "audiobookshelf": "lifestyle",
	"calibre-web": "lifestyle", "class-schedule": "lifestyle", "cloud-bijia": "lifestyle",
	"com.bitxeno.atvloadly": "lifestyle", "com.trek.app": "lifestyle", "daymark": "lifestyle",
	"docker-home-assistantan": "lifestyle", "ech0": "lifestyle", "ermao-books": "lifestyle",
	"ezbookkeeping": "lifestyle", "feilv-reader": "lifestyle", "fn-88jizhang": "lifestyle",
	"fn-reader": "lifestyle", "fnos-app-health-records": "lifestyle", "freshrss": "lifestyle",
	"fusion": "lifestyle", "goodgym": "lifestyle", "home-assistant": "lifestyle",
	"homeassistant": "lifestyle", "homebox": "lifestyle", "koodo-reader": "lifestyle",
	"lanraragi": "lifestyle", "leelaa.reader": "lifestyle", "lijin": "lifestyle",
	"lite.class": "lifestyle", "lite.noise": "lifestyle", "lite.reader": "lifestyle",
	"mealie": "lifestyle", "minibill": "lifestyle", "miniflux": "lifestyle",
	"mmh": "lifestyle", "moyue": "lifestyle", "nas-reader-fnos": "lifestyle",
	"pickup.codes": "lifestyle", "pregnancyjournal": "lifestyle", "reader": "lifestyle",
	"rewardhub": "lifestyle", "sbti": "lifestyle", "sinong": "lifestyle",
	"soyue-go": "lifestyle", "suwayomi": "lifestyle", "talebook": "lifestyle",
	"techfunway-bill": "lifestyle", "techfunway-lottery": "lifestyle", "ting-reader": "lifestyle",
	"trek": "lifestyle", "vaultwarden": "lifestyle", "zonefoundry-bridge": "lifestyle",
	// efficiency (106)
	"angemedia": "efficiency", "angevoice": "efficiency", "app.native.mdeditor2": "efficiency",
	"appflowy": "efficiency", "bestnav": "efficiency", "bili-sync": "efficiency",
	"cloud-members": "efficiency", "couple-relay-web": "efficiency", "cryptbox": "efficiency",
	"dev.xuanran.timetrace": "efficiency", "docker-affine": "efficiency", "docker-halo": "efficiency",
	"docker-heimdall": "efficiency", "docker-homepage": "efficiency", "docker-qianniuyibang": "efficiency",
	"docker-siyuan": "efficiency", "docker-wizserver": "efficiency", "docsify": "efficiency",
	"drawnix": "efficiency", "dufs": "efficiency", "dupclean": "efficiency",
	"easyprinter": "efficiency", "excalidraw": "efficiency", "fastnotesync": "efficiency",
	"feigram": "efficiency", "file-collector": "efficiency", "file-tools": "efficiency",
	"file.dedupe": "efficiency", "filebrowser": "efficiency", "fileview": "efficiency",
	"flatnas": "efficiency", "flymail": "efficiency", "fn-memos": "efficiency",
	"fn_qyzb": "efficiency", "fnchat": "efficiency", "fnnas.jisiyu": "efficiency",
	"fnnas.notes": "efficiency", "fnnas.zdinnav": "efficiency", "fnos-app-shutdown": "efficiency",
	"glance": "efficiency", "greaterwms": "efficiency", "hand.nas": "efficiency",
	"homarr": "efficiency", "homepage": "efficiency", "iconstation": "efficiency",
	"la-biz": "efficiency", "la-erp-lite": "efficiency", "leelaa.glbload": "efficiency",
	"leelaa.pdfload": "efficiency", "linker": "efficiency", "linkwarden": "efficiency",
	"lite.parser": "efficiency", "litenav": "efficiency", "llm-proofread": "efficiency",
	"lpsi": "efficiency", "m.text.editor": "efficiency", "madopic": "efficiency",
	"magicmail": "efficiency", "mail-archiver": "efficiency", "markdown": "efficiency",
	"md-editor-fn": "efficiency", "memos": "efficiency", "minipaint": "efficiency",
	"nextexplorer": "efficiency", "note": "efficiency", "notepad": "efficiency",
	"nowen-note": "efficiency", "obsidian": "efficiency", "oknote": "efficiency",
	"omni-tools": "efficiency", "paddle-formula-ocr": "efficiency", "paint-board": "efficiency",
	"pandasurvey": "efficiency", "panorama": "efficiency", "paperless-ngx": "efficiency",
	"papersplit": "efficiency", "pcm": "efficiency", "pdf-covert": "efficiency",
	"penpot": "efficiency", "puter": "efficiency", "reactive-resume": "efficiency",
	"remindflow": "efficiency", "shop": "efficiency", "siyuan": "efficiency",
	"stirling-pdf": "efficiency", "stirlingpdf": "efficiency", "sun-panel": "efficiency",
	"surveyking": "efficiency", "te-searxng": "efficiency", "te-tieniu-memos": "efficiency",
	"teamspeaker": "efficiency", "teamspeaker6": "efficiency", "techfunway-memo": "efficiency",
	"techfunway-notepad": "efficiency", "techfunway-reminders": "efficiency", "techfunway.bookmarks": "efficiency",
	"trim.docs": "efficiency", "trim.preview": "efficiency", "trim.text-editor": "efficiency",
	"typecho": "efficiency", "vikunja": "efficiency", "vocechat-server": "efficiency",
	"wechat-on-cloud": "efficiency", "wikijs": "efficiency", "wordmbdy": "efficiency",
	"xboard": "efficiency",
	// 跨源描述差异统一（2026-09-26 审计补充）
	"fntermx": "devtools", // 终端（个别源简介为 base64 图片垃圾数据）
	"read": "lifestyle",    // 轻阅读（个别源为 APP 后端变体）
	// driver (15)
	"amd-container-toolkit": "driver", "amd.rocm-7.2": "driver", "binder_linux_driver": "driver",
	"fn-aic8800": "driver", "fn-broadcom-sta": "driver", "fn-ntfs3": "driver",
	"fn-open-vm-tools": "driver", "fn-qemu-ga": "driver", "i915-sriov-dkms_driver": "driver",
	"ite-it87_driver": "driver", "nvidia-container-toolkit": "driver", "nvidia-driver-580": "driver",
	"surface-battery": "driver", "trim.ai-runtime-amd-migraphx": "driver", "trim.ai-runtime-amd-vitisai": "driver",
}

// labelCategoryMap 源原始标签 → 13 键。官方 tags 与 UI 类别一一对应；
// fndepot 中文标签按官方领域口径映射。
var labelCategoryMap = map[string]string{
	// 官方 10 类（一一对应）
	"实用效率": "efficiency", "开发工具": "devtools", "生活服务": "lifestyle",
	"影音娱乐": "media", "游戏": "game", "AI": "ai", "备份同步": "backup",
	"摄影摄像": "photo", "驱动": "driver", "下载": "download",
	// 英文原名（面板 API 部分环境返回英文）
	"Practical Efficiency": "efficiency", "Development Tools": "devtools",
	"Lifestyle": "lifestyle", "Audio & Video Entertainment": "media",
	"Games": "game", "Backup Sync": "backup", "Photography & Video": "photo",
	"Drive": "driver",
	// fndepot 中文标签
	"影音": "media", "娱乐": "media", "音乐": "media", "视频": "media",
	"影视": "media", "照片": "photo", "游戏地带": "game",
	"AI赋能": "ai", "人工智能": "ai", "ai": "ai",
	"备份": "backup", "同步": "backup", "网盘": "backup", "云盘": "backup",
	"快照": "backup",
	"传输": "download", "下载传输": "download",
	"网络": "network", "浏览器": "browser",
	"编程开发": "devtools", "编程": "devtools", "开发": "devtools",
	"工具": "devtools", "系统工具": "devtools", "安全": "devtools",
	"硬件驱动": "driver",
	"智能智控": "lifestyle", "教育学习": "lifestyle", "教育": "lifestyle",
	"学习": "lifestyle", "阅读": "lifestyle", "图书": "lifestyle",
	"办公": "efficiency", "效率": "efficiency", "内容管理": "efficiency",
	// conversun 遗留 8 键（旧目录兼容）
	"system": "devtools", "store": "devtools",
	// 歧义值不映射，交给关键词
}

type classifyRule struct {
	key       string
	strong    []string // 全文子串（小写后），领域特征词
	words     []string // 全文英文词边界匹配
	namekw    []string // 仅 appname+显示名子串（通用词）
	namewords []string // 仅 appname+显示名英文词边界
}

// 优先级：特征强的先。photo 在 media 前（照片/摄像域更专）；
// game 在 media 前（游戏直播→游戏可接受）；network 在 backup/download
// 前（网络存储→备份同步由「网盘/云盘」词保证，连通词优先命中网络）；
// devtools 在 lifestyle/efficiency 前（服务器/容器域特征强）；
// driver 垫底（最专、最少）。
var classifyRules = []classifyRule{
	{
		key: "ai",
		strong: []string{
			"大模型", "大语言模型", "文生图", "图生图", "文生视频", "图片生成",
			"视频生成", "图像生成", "语音合成", "语音识别", "语音转文字", "数字人",
			"虚拟人", "智能体", "机器学习", "深度学习", "人工智能", "换脸", "音色克隆",
			"语音模型", "公式识别", "ai聊天", "ai助手", "ai助理", "ai绘画", "ai绘图",
			"ai生成", "ai翻译", "ai办公", "ai学习", "ai搜索", "ai相机", "ai模型",
			"模型推理", "openai", "anthropic", "open-webui", "openwebui",
			"stable diffusion", "sd-webui", "comfyui", "whisper", "ollama",
			"dify", "vllm", "midjourney", "langchain", "mediapipe", "openvino",
			"deepseek", "qwen", "chatbox", "lm studio", "llm",
		},
		words:     []string{"gpt", "tts", "asr", "mcp", "llama", "mistral", "claude", "gemini", "copilot", "npu", "aigc", "agent"},
		namewords: []string{"ai"},
	},
	{
		// 严格限 *arr 套件 + 字幕/追番索引工具（官方 *arr 归影音娱乐，
		// 此处细化为媒体自动化新类别）
		key: "automation",
		strong: []string{
			"sonarr", "radarr", "lidarr", "readarr", "bazarr", "tautulli",
			"jackett", "prowlarr", "chinesesubfinder", "auto-bangumi",
			"autobangumi", "bangumi", "追番", "影视自动化", "字幕", "scraper", "索引器",
			"直播录制", "自动录制",
		},
	},
	{
		key: "browser",
		strong: []string{
			"网页浏览器", "浏览器内核", "浏览器扩展", "浏览器插件", "无头浏览器",
			"web browser", "headless browser", "firefox", "chromium", "vivaldi",
		},
		words:     []string{"browser"},
		namekw:    []string{"浏览器"},
		namewords: []string{"browser", "firefox", "chromium"},
	},
	{
		key: "game",
		strong: []string{
			"游戏", "棋牌", "麻将", "消消乐", "赛车", "手游", "小游戏", "网页游戏",
			"游戏串流", "minecraft",
			"我的世界", "palworld", "mcsmanager", "gsmanager", "gamepad",
		},
		words:  []string{"game", "games"},
		namekw: []string{"游戏"},
	},
	{
		key: "photo",
		strong: []string{
			"相机", "摄像", "摄像机", "摄像头", "nvr", "监控录像",
			"视频监控", "拍照", "影像",
		},
		namekw: []string{"照片", "相册", "相机"},
	},
	{
		key: "media",
		strong: []string{
			"jellyfin", "emby", "plex", "kodi", "airplay", "chromecast", "komga",
			"kavita", "sunshine", "navidrome", "koel", "seerr", "ombi", "wizarr",
			"media server", "video player", "music player", "media player",
			"streaming", "音乐服务器", "播放器", "电台",
			"收音机", "投屏", "流媒体服务器", "在线播放", "观影", "追剧", "直播", "弹幕",
			"影音", "影视",
		},
		words:  []string{"mpd", "mpv", "vlc", "vr", "music"},
		namekw: []string{"视频", "音乐", "电影", "电视", "媒体", "影音", "media", "music"},
	},
	{
		key: "network",
		strong: []string{
			"vpn", "v2ray", "clash", "shadowsocks", "wireguard", "tailscale",
			"zerotier", "frp", "cloudflared", "tunnel", "隧道", "内网穿透",
			"组网", "路由", "旁路由", "软路由", "防火墙", "广告拦截", "测速",
			"镜像加速", "网络加速", "交换机", "网络工具", "网络共享", "网络检测", "网络调试",
			"公网", "mesh", "rustdesk", "远程控制", "remote desktop", "ddns", "订阅链接", "订阅管理",
			"adguard", "pihole", "mosdns", "smartdns", "mihomo", "easytier",
			"netbird", "headscale", "wg-easy", "唤醒",
			"远程桌面", "虚拟局域网",
		},
		words:  []string{"dns", "router", "speedtest", "intranet"},
		namekw: []string{"网络"},
	},
	{
		key: "backup",
		strong: []string{
			"备份", "网盘", "云盘", "云存储", "快照", "私有云", "alist", "nextcloud",
			"seafile", "syncthing", "resilio",
		},
		words:  []string{"sync", "backup"},
		namekw: []string{"备份", "同步", "网盘", "云盘", "sync"},
	},
	{
		key: "download",
		strong: []string{
			"离线下载", "种子", "磁力", "局域网传输", "文件快递", "图床",
			"aria2", "qbit", "transmission",
			"torrent", "emule", "webdav", "localsend", "cloud drive", "thunder",
			"icloud", "onedrive", "dropbox", "youtube", "yt-dlp", "sabnzbd",
			"nzb", "rclone", "p2p", "迅雷", "xunlei", "motrix", "m3u8",
		},
		words:  []string{"bt", "download", "uploader", "ftp", "samba", "nfs", "rsync"},
		namekw: []string{"下载", "上传", "传输", "下载器"},
	},
	{
		key: "devtools",
		strong: []string{
			"docker", "kubernetes", "k8s", "k3s", "prometheus", "grafana",
			"btrfs", "kvm", "qemu", "proxmox", "容器", "镜像", "面板",
			"监控", "告警", "日志", "终端", "磁盘", "硬盘", "数据恢复", "运维",
			"虚拟机", "虚拟", "文件系统", "存储池", "杀毒", "病毒", "antivirus",
			"开发环境", "应用商店", "应用仓库", "软件仓库", "应用中心", "通知",
			"数据库", "工具箱", "定时任务", "脚本", "反向代理", "代码托管",
			"代码", "git", "gitea", "forgejo", "node-red", "n8n", "nginx",
			"caddy", "traefik", "web server", "vscode", "vs code", "堡垒机",
			"工作流", "驱动开发", "jdk", "即时通讯", "团队沟通", "团队协作",
		},
		words:  []string{"ssh", "panel", "monitor", "terminal", "system", "gotify", "ntfy", "git"},
		namekw: []string{"开发", "编程", "系统"},
	},
	{
		key: "lifestyle",
		strong: []string{
			"智能家居", "家居", "钢琴", "健身", "记账", "预算", "日记", "阅读",
			"图书", "读书", "电子书", "漫画", "有声书", "证件照", "医疗", "健康",
			"教育", "课程", "课堂", "学习", "食谱", "菜谱", "亲子", "旅行",
			"rss", "播客", "podcast",
		},
		namekw: []string{"日记", "阅读", "读书"},
	},
	{
		key: "efficiency",
		strong: []string{
			"笔记", "文档", "知识库", "博客", "日历", "日程", "待办", "办公",
			"效率", "简历", "剪贴板", "导航", "仪表盘", "聊天", "telegram",
			"微信", "书签", "画板", "白板", "绘图", "图像编辑", "密码", "认证",
			"证书", "邮件", "进销存", "文件管理", "文件预览", "webdav", "epub",
			"notion", "obsidian", "wiki", "blog", "todo", "notepad", "journal",
			"calibre", "markdown", "转码", "设计", "原型", "figma", "问卷", "考试",
		},
		words: []string{"pdf", "ebook", "calendar", "schedule", "office", "im"},
	},
	{
		key: "driver",
		// 收紧：裸「驱动/显卡/内核/amd」子串误伤（"由 Rust 驱动"、"无需显卡"、
		// "AMD NPU 推理 Runtime"），只认驱动特征短语与芯片厂牌。
		strong: []string{
			"驱动包", "驱动程序", "显卡驱动", "gpu 驱动", "电池驱动", "内核驱动",
			"内核模块", "nvidia", "rocm", "dkms", "固件", "vm tools", "guest agent",
		},
		words:     []string{"driver", "dkms"},
		namekw:    []string{"驱动"},
		namewords: []string{"driver", "nvidia", "rocm"},
	},
}

// identityKeywords 应用身份词（产品名级）：第一遍匹配，命中即定领域，
// 不受规则顺序影响。解决「次要功能短语误伤」——如 nginx 简介提「支持流
// 媒体」被 media 规则抢走（2026-09-26 nginx 跨源归类不一致实锤）。
var identityKeywords = []struct {
	key string
	kw  []string
}{
	{"devtools", []string{"nginx", "apache", "traefik", "gitea", "forgejo", "phpmyadmin", "code-server"}},
	{"media", []string{"jellyfin", "emby", "plex", "kodi", "navidrome", "komga", "kavita"}},
	{"network", []string{"mihomo", "v2ray", "clash", "tailscale", "zerotier", "cloudflared", "frp"}},
	{"download", []string{"qbittorrent", "aria2", "transmission", "bitcomet", "motrix", "xunlei"}},
	{"browser", []string{"firefox", "chromium", "msedge", "microsoft edge"}},
	{"driver", []string{"nvidia", "rocm", "dkms"}},
	{"ai", []string{"ollama", "open-webui", "openwebui", "stable diffusion", "comfyui"}},
	{"automation", []string{"sonarr", "radarr", "lidarr", "readarr", "bazarr"}},
}

var wordReCache = map[string]*regexp.Regexp{}

func wordRe(word string) *regexp.Regexp {
	if r, ok := wordReCache[word]; ok {
		return r
	}
	r := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
	wordReCache[word] = r
	return r
}

// classifyApp 单个应用的归类（空 = 无法归类，仅出现在「全部」）。
// 优先级：关键词（应用实际领域，按名称+简介）→ 源标签映射（兜底）。
// 2026-09-26 审计结论：fndepot 第三方源的标签由仓库作者随手打（常整库一个
// 标签），不可信；官方/conversun 应用已全部进精选表，标签仅对未知应用兜底。
func classifyApp(appName, displayName, desc, rawCategory string) string {
	// 防护：部分 fndepot 源把 base64 图片当简介（长串字母数字会随机撞上
	// 身份词），截取到 <img data-uri 之前
	if i := strings.Index(desc, "<img src='data:"); i >= 0 {
		desc = desc[:i]
	}
	name := strings.ToLower(strings.TrimSpace(appName + " " + displayName))
	text := strings.ToLower(strings.TrimSpace(appName + " " + displayName + " " + desc))
	// 第一遍：产品身份词（命中即定，不受规则顺序影响）
	for _, id := range identityKeywords {
		for _, kw := range id.kw {
			if strings.Contains(text, kw) {
				return id.key
			}
		}
	}
	for _, rule := range classifyRules {
		for _, kw := range rule.strong {
			if strings.Contains(text, kw) {
				return rule.key
			}
		}
		for _, w := range rule.words {
			if wordRe(w).MatchString(text) {
				return rule.key
			}
		}
		for _, kw := range rule.namekw {
			if strings.Contains(name, kw) {
				return rule.key
			}
		}
		for _, w := range rule.namewords {
			if wordRe(w).MatchString(name) {
				return rule.key
			}
		}
	}
	// 关键词无法判定时，回退源标签（仅对未收录精选表的未知应用生效）
	if c, ok := labelCategoryMap[strings.TrimSpace(rawCategory)]; ok {
		return c
	}
	return ""
}

// classifyAll 目录组装末尾的统一后处理：
// 精选表（官方+conversun 人工归类）最高优先级跨源统一；有效 13 键保留；
// 其余走 标签映射 → 关键词 归类。
func classifyAll(out []AppInfo) {
	for i := range out {
		ai := &out[i]
		if c, ok := curatedCategories[strings.ToLower(strings.TrimSpace(ai.AppName))]; ok {
			ai.Category = c
			continue
		}
		if validCategoryKeys[ai.Category] {
			continue
		}
		ai.Category = classifyApp(ai.AppName, ai.DisplayName, ai.Description, ai.Category)
	}
}
