# qqmsg3-viewer

这是一个用 Golang 和 HTML5 写的 QQ 聊天记录查看小工具。

## 声明

- 本项目是 AI 产物。
- 此程序需要搭配**已经解码**的 `Msg3.0.db` 使用。如果你没有或者不知道这个是什么，请右转 [qqmsg3-decoder](https://github.com/yuko1225/qqmsg3-decoder)。
- 本项目不对生成内容的完整性、准确性作任何担保，生成的一切内容不可用于法律取证，你不应当将其用于学习与交流外的任何用途。

## 预览
<img width="1393" height="821" alt="图片" src="https://github.com/user-attachments/assets/79736f21-e03e-40e2-957a-267fc31c0862" />


## 简单介绍

这个工具主要是在电脑上方便看之前的聊天记录，尤其是已经退出的群聊记录：
- 支持跨平台运行（Windows、macOS、Linux 都可以）。
- 界面类似WX/QQ，支持分左右气泡显示。
- 速度很快 比起QQ自带的要快得多
- 可以按时间跳转、搜关键词，或者按发信人筛选消息。
- **支持导出为 MHTML** 格式。
- 能显示系统自带表情（需要自己提供表情图片文件）和聊天图片（需要配置好图片目录）。
- 头像会尝试从 API 自动获取并缓存下来。

## 技术栈

- **后端**: Golang (`modernc.org/sqlite`, `go-chi/chi`, `BurntSushi/toml`)
- **前端**: HTML5

## 文件结构

```text
qqviewer/
├── cmd/main.go                # 程序入口
├── internal/                  # 内部代码
│   ├── avatar/avatar.go       
│   ├── config/config.go       
│   ├── db/db.go               
│   ├── handler/handler.go     
│   └── parser/parser.go       
├── static/                    # 静态资源 (CSS, JS, 图片)
│   ├── css/main.css
│   ├── js/chat.js
│   └── js/index.js
├── templates/                 
│   └── index.html
├── config.conf                # 配置文件
├── go.mod
├── go.sum
└── README.md
```

## 怎么用

1. **准备数据库**: 准备一份已经解密并处理过的Msg3.0.db
2. **改配置**: 把 `config.conf.example` 复制一份并改名为 `config.conf`，然后改一下里面的内容：
   - `database.path`: 你的数据库文件路径。
   - `user.my_qq`: 填你自己的 QQ 号，用来区分左右气泡。
   - `images.base_dir` (可选): 如果你想看聊天里的图片，把这个指到图片所在的父目录。
3. **运行**: 直接运行编译好的文件。
   - Windows 运行 `qqviewer.exe`
   - macOS 和 Linux 运行 `./qqviewer`
4. **查看聊天记录**: 浏览器打开 `http://127.0.0.1:8080` 就行了。

## 编译

如果想自己编译，需要 Go（1.25 以上版本）。

```bash
# 安装依赖
go mod tidy

# 编译
go build -o qqviewer ./cmd/main.go
```

## Q&A

**Q：程序被某杀毒软件报毒了怎么办？**  
A：程序源代码完全开放，你可以按照上面的编译方式自己编译。看不懂代码可以去问 AI。

**Q：既然是 AI 产物，为什么还要发出来？**  
A：给你们省 token，~~顺带污染 AI 训练集~~。

**Q：为什么要开发这个工具？**  
A：为了看已经退出/解散的群聊记录，QQ从某个版本开始就不再让你查看这些记录了。但其实他们依旧存在在数据库中，只是不再显示。

**Q：Msg2.0.db怎么办？**  
A：请先使用QQ自带的工具导入到3.0数据库中，详见qqmsg3-decoder的说明。

**Q：为什么昵称看不到？**  
A：QQ 的接口获取昵称太困难，要是你有好的接口可以提 issue。

## 参与贡献

欢迎提 PR，但请确保你提的代码你自己看得懂。

## License

MIT
