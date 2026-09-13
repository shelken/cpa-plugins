## 约束

- 决定不出现任何逆向工具
- 从`pi-codebuudy-provider`进行静态字段的和代码分析进行移植
- 任何工具脚本从`pi-codebuddy-provider`阅读和修改

## 注意点

- 当初pi-codebuddy-provider 调用的请求头全部都是从cli进行获取的, oauth也是按照cli来的. 因此才有这些差异. 而当前这个cpa插件对齐桌面端.

## 参考仓库

- [shelken/pi-codebuudy-provider](https://github.com/shelken/pi-codebuudy-provider): 一般在本地的`{active-dir}/pi-codebuddy-provider`
