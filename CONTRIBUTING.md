# Contributing

本项目已停止维护，代码仅供学习、研究和实现参考。

当前仓库不接受 Issue、Pull Request 或功能请求。使用过程中遇到的问题请自行排查和处理。

如果你希望继续开发，请 fork 本仓库后在自己的仓库中修改和维护。

## 发布者说明

仓库维护者发布代码时建议先执行：

```bash
rg -n -S "(API_KEY|SECRET|PASSWORD|TOKEN|PRIVATE KEY|sk-[A-Za-z0-9])" .

cd cursor-client
go test ./...

cd ../cursor-tap-ref
go test ./...
```
