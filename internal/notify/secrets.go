package notify

import (
	"log"

	"moo/internal/config"
)

// SealChannelSecrets 就地加密（seal=true）/ 解密（seal=false）各渠道参数里的
// **敏感字段**（`Field.Sensitive`：webhook_url / secret / sendkey / token /
// device_key 等），由 config.SetExtraCodec 注册调用——放在本包是为了避开
// config → notify 的循环依赖。
//
// 返回值 foundPlaintext：解密方向遇到「明文值」时为 true（存量配置），
// 上层据此触发一次 Save 把它改写为密文。
//
// 解密失败（换机器/密钥文件丢失）时：该字段清空 + 打 SecretDecryptFailed 标记
// + 记日志，绝不 panic；表现为该渠道发不出去，用户在设置页重填即可。
func SealChannelSecrets(cfg *config.Config, seal bool) (bool, error) {
	foundPlaintext := false
	for i := range cfg.NotifyChannels {
		ch := &cfg.NotifyChannels[i]
		def := ChannelDefByKey(ch.Type)
		if def == nil || len(ch.Params) == 0 {
			continue
		}
		for _, f := range def.Fields {
			if !f.Sensitive {
				continue
			}
			v := ch.Params[f.Key]
			if v == "" {
				continue
			}
			if seal {
				if config.IsSealedValue(v) {
					continue // 已是密文，避免二次加密
				}
				sealed, err := config.SealValue(v)
				if err != nil {
					return foundPlaintext, err
				}
				ch.Params[f.Key] = sealed
				continue
			}
			if !config.IsSealedValue(v) {
				foundPlaintext = true // 存量明文：原样保留，等 Save 时加密
				continue
			}
			plain, err := config.OpenValue(v)
			if err != nil {
				log.Printf("通知渠道「%s」的 %s 解密失败，已清空（请在设置页重新填写）: %v",
					ch.Name, f.Key, err)
				ch.Params[f.Key] = ""
				cfg.SecretDecryptFailed = true
				continue
			}
			ch.Params[f.Key] = plain
		}
	}
	return foundPlaintext, nil
}
