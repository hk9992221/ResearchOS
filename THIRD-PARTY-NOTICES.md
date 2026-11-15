# 第三方许可声明（Third-Party Notices）

ResearchOS 随二进制分发的全部开源组件、许可证、以及安装包的义务清单。
许可证全文在 `licenses/`，逐模块的精确版本在 `licenses/MANIFEST.tsv`；
改过 `go.mod` 或新增内嵌资源后跑 `scripts/collect-licenses.sh` 重新生成，
该脚本会在"链接进来但没登记"或"登记了但不再链接"时报错退出。

分发内容分三类：**18 个 Go 模块 + Go 运行时**（静态链接进 `researchos.exe`）、
**6 个字体文件**（3 个 STIX Two OTF 与 3 个 BaKoMa Computer Modern TTF，`go:embed`
进二进制）、**42 个 Lucide 图标**（SVG 路径写在源码里）。

## 1. Go 依赖

| 组件 | 许可证 | 在项目里的用途 | 文本 |
|---|---|---|---|
| github.com/egoist/mygo | MIT | UI 工具包：窗口、布局、文本整形、WebView、可访问性 | `mygo__LICENSE.txt` |
| github.com/go-text/typesetting | Unlicense OR BSD-3-Clause（双许可，任选） | 文本整形引擎（mygo 依赖） | `typesetting__LICENSE.txt` |
| github.com/ebitengine/purego | Apache-2.0 | 无 cgo 的 FFI，mygo 调 DirectWrite / COM | `purego__LICENSE.txt` |
| github.com/BurntSushi/toml | MIT | 实验元数据 `.toml` 解析 | `toml__COPYING.txt` |
| github.com/dustin/go-humanize | MIT | 字节数可读化（modernc libc 依赖） | `humanize__LICENSE.txt` |
| github.com/ledongthuc/pdf | BSD-3-Clause | PDF 文本提取，供搜索索引 | `pdf__LICENSE.txt` |
| github.com/mattn/go-isatty | MIT | 终端判定（modernc libc 依赖） | `isatty__LICENSE.txt` |
| github.com/ncruces/go-strftime | MIT | strftime 格式化（modernc libc 依赖） | `go-strftime__LICENSE.txt` |
| github.com/remyoudompheng/bigfft | BSD-3-Clause | 大整数 FFT（modernc mathutil 依赖） | `bigfft__LICENSE.txt` |
| golang.org/x/image | BSD-3-Clause | 位图处理（mygo `ui/bitmap.go`） | `x-image__LICENSE.txt` |
| golang.org/x/net | BSD-3-Clause | HTML 解析（mygo richtext） | `x-net__LICENSE.txt` |
| golang.org/x/sys | BSD-3-Clause | 系统调用与 DLL 加载 | `x-sys__LICENSE.txt` |
| golang.org/x/text | BSD-3-Clause | 语言标签、Unicode 归一化与全半角（i18n、搜索） | `x-text__LICENSE.txt` |
| gopkg.in/yaml.v3 | MIT + Apache-2.0（双许可） | 实验元数据 `.yaml`/`.yml` 解析 | `yaml.v3__LICENSE.txt`、`yaml.v3__NOTICE.txt` |
| modernc.org/sqlite | BSD-3-Clause；内含 SQLite（公有领域）与 sqlite-vec（MIT） | 项目数据库引擎（纯 Go，无外部 DLL） | `sqlite__LICENSE.txt`、`sqlite__LICENSE-SQLITE.txt`、`sqlite__LICENSE-SQLITE_VEC.txt`、`sqlite__LICENSE-3RD-PARTY.txt` |
| modernc.org/libc | BSD-3-Clause；第三方含 musl 等（见文件） | SQLite 移植所需的 C 运行时 | `libc__LICENSE.txt`、`libc__LICENSE-3RD-PARTY.txt` |
| modernc.org/mathutil | BSD-3-Clause | SQLite 的大数与随机数 | `mathutil__LICENSE.txt` |
| modernc.org/memory | BSD-3-Clause；另含 Go 运行时 BSD、mmap-go BSD | 内存与 mmap 管理 | `memory__LICENSE.txt`、`memory__LICENSE-GO.txt`、`memory__LICENSE-MMAP-GO.txt`、`memory__LICENSE-LOGO.txt` |
| Go 运行时与标准库 | BSD-3-Clause | 静态链接进每个二进制 | `go-runtime__LICENSE.txt` |

`go.sum` 里的其余模块（音频解码、cc/ccgo 等编译期工具链、测试依赖）没有进入
`./cmd/...` 的链接图，不随安装包分发，因此不在上表；脚本按链接图判定，
依赖变化时会自动要求补登记。

## 2. 内嵌字体

| 字体 | 许可证 | 位置 | 文本 |
|---|---|---|---|
| STIX Two Math Regular、STIX Two Text Italic、STIX Two Text SemiBold（未修改原件，取自 CTAN `fonts/stix2-otf`） | SIL Open Font License 1.1，保留字体名为 **"TM Math"** | `internal/ui/formula/fonts/*.otf`，公式排版 | `fonts__STIX-Two__OFL.txt` |
| BaKoMa Computer Modern：cmr10、cmmi10、cmbx10（CTAN `fonts/cm/ps-type1/bakoma/ttf`） | BaKoMa Fonts Licence（免费复制/分发/使用，须随件带版权声明与许可文本；使用不需声明） | `internal/ui/formula/fonts/*.ttf`，公式回退字形 | `fonts__BaKoMa-CM__LICENCE.txt` |

## 3. 图标

`internal/ui/icons/icons.go` 内联 42 个 Lucide 线性图标（24 单位、2 单位描边）的
SVG 路径数据。许可证 ISC；其中一部分派生自 Feather（MIT），两段文本都在
`icons__Lucide__LICENSE.txt` 里，一并分发即满足要求。

## 4. 不随安装包分发，无需声明

- **只按名字引用的系统字体**：Inter、Segoe UI、Helvetica Neue、Arial、
  JetBrains Mono、Cascadia Code、Consolas、Microsoft YaHei UI、PingFang SC、
  Noto Sans CJK SC、Cambria Math、Latin Modern Math、Times New Roman、DejaVu Serif。
  程序不复制、不打包这些字体；用户机器上没有时按字体栈回退。
- **WebView2 Runtime**（`EmbeddedBrowserWebView.dll`）：mygo 直接加载系统已装的
  运行时，安装包不带 Loader DLL，也不重新分发 Edge。
- **DirectWrite / GDI / Win32 API**：Windows 系统组件，调用不构成分发。

## 5. 安装包义务清单

- [ ] `licenses/` 整个目录随包（安装目录 `licenses\` 或"关于 → 第三方许可"页可读）。
- [ ] 字体字节与 `fonts__STIX-Two__OFL.txt`、`fonts__BaKoMa-CM__LICENCE.txt` 同包：
      OFL 第二条要求每份副本带版权说明与许可文本，BaKoMa 同理。
- [ ] 不得把字体单独出售；必须与软件一起分发（OFL 第一条）。
- [ ] 不修改字体文件，因此无需改名；若将来做子集化（subsetting），属"修改版"，
      不得再使用保留名 "TM Math"，并需注明修改。
- [ ] Apache-2.0 组件（purego、yaml.v3 的 Apache 部分）随包带 `yaml.v3__NOTICE.txt`；
      我们未修改其源码，无需列改动。
- [ ] MIT / BSD / ISC 组件保留版权行与许可文本原文（`licenses/` 已按原文复制）。
- [ ] 本项目自身还没有根 `LICENSE` 文件：闭源发行需写明专有许可，开源发行需选定
      许可证，两者都不影响以上第三方义务。
