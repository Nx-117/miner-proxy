package main

import (
	_ "embed"
	"fmt"
	app2 "miner-proxy/app"
	"miner-proxy/pkg"
	"miner-proxy/pkg/middleware"
	"miner-proxy/proxy/client"
	"miner-proxy/proxy/server"
	"miner-proxy/proxy/wxPusher"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/denisbrodbeck/machineid"
	"github.com/gin-gonic/gin"
	"github.com/jmcvetta/randutil"
	"github.com/kardianos/service"
	"github.com/liushuochen/gotable"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"go.uber.org/zap/zapcore"
)

var (
	// build 时加入
	gitCommit string
	version   string
	//go:embed web/index.html
	indexHtml []byte
)
var (
	reqeustUrls = []string{
		"https://www.baidu.com/",
		"https://m.baidu.com/",
		"https://www.jianshu.com/",
		"https://www.jianshu.com/p/4fbdab9fb44c",
		"https://www.jianshu.com/p/5d25218fb22d",
		"https://www.tencent.com/",
		"https://tieba.baidu.com/",
	}
)

type proxyService struct {
	args *cli.Context
}

func (p *proxyService) checkWxPusher(wxPusherToken string, newWxPusherUser bool) error {
	if len(wxPusherToken) <= 10 {
		pkg.Fatal("您输入的微信通知token无效, 请在 https://wxpusher.zjiecode.com/admin/main/app/appToken 中获取")
	}
	w := wxPusher.NewPusher(wxPusherToken)
	if newWxPusherUser {
		qrUrl, err := w.ShowQrCode()
		if err != nil {
			pkg.Fatal("获取二维码url失败: %s", err.Error())
		}
		fmt.Printf("请复制网址, 在浏览器打开, 并使用微信进行扫码登陆: %s\n", qrUrl)
		pkg.Input("您是否扫描完成?(y/n):", func(s string) bool {
			if strings.ToLower(s) == "y" {
				return true
			}
			return false
		})
	}

	users, err := w.GetAllUser()
	if err != nil {
		pkg.Fatal("获取所有的user失败: %s", err.Error())
	}
	table, _ := gotable.Create("uid", "微信昵称")
	for _, v := range users {
		_ = table.AddRow(map[string]string{
			"uid":  v.UId,
			"微信昵称": v.NickName,
		})
	}
	fmt.Println("您已经注册的微信通知用户, 如果您还需要增加用户, 请再次运行 ./miner-proxy -add_wx_user -wx tokne, 增加用户, 已经运行的程序将会在5分钟内更新订阅的用户:")
	fmt.Println(table.String())
	if !p.args.Bool("c") && (p.args.String("l") != "" && p.args.String("k") != "") {
		// 不是客户端并且不是只想要增加新的用户, 就直接将wxpusher obj 注册回调
		if err := server.AddConnectErrorCallback(w); err != nil {
			pkg.Fatal("注册失败通知callback失败: %s", err.Error())
		}
	}
	return nil
}

func (p *proxyService) startHttpServer() {
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	app.Use(gin.Recovery(), middleware.Cors())

	skipAuthPaths := []string{
		"/download/",
	}

	if p.args.String("p") != "" {
		middlewareFunc := gin.BasicAuth(gin.Accounts{
			"admin": p.args.String("p"),
		})
		app.Use(func(ctx *gin.Context) {
			for _, v := range skipAuthPaths {
				if strings.HasPrefix(ctx.Request.URL.Path, v) {
					return
				}
			}
			middlewareFunc(ctx)
			return
		})
	}

	port := strings.Split(p.args.String("l"), ":")[1]

	app.Use(func(ctx *gin.Context) {
		ctx.Set("tag", version)
		ctx.Set("secretKey", p.args.String("k"))
		ctx.Set("server_port", port)
		ctx.Set("download_github_url", p.args.String("g"))
	})

	app2.NewRouter(app)

	app.NoRoute(func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html", indexHtml)
	})

	pkg.Info("web server address: %s", p.args.String("a"))
	if err := app.Run(p.args.String("a")); err != nil {
		pkg.Panic(err.Error())
	}
}

func (p *proxyService) Start(_ service.Service) error {
	go p.run()
	return nil
}

func (p *proxyService) randomRequestHttp() {
	defer func() {
		sleepTime, _ := randutil.IntRange(10, 60)
		time.AfterFunc(time.Duration(sleepTime)*time.Second, p.randomRequestHttp)
	}()

	index, _ := randutil.IntRange(0, len(reqeustUrls))
	pkg.Debug("request: %s", reqeustUrls[index])
	resp, _ := (&http.Client{Timeout: time.Second * 10}).Get(reqeustUrls[index])
	if resp == nil {
		return
	}
	_ = resp.Body.Close()
}

func (p *proxyService) run() {
	defer func() {
		if err := recover(); err != nil {
			pkg.Error("程序崩溃: %v, 重启中", err)
			p.run()
		}
	}()

	if !p.args.Bool("c") {
		fmt.Printf("监听端口 '%s', 默认矿池地址: '%s'\n", p.args.String("l"), p.args.String("r"))
	}

	if p.args.Bool("d") {
		pkg.Warn("你开启了-debug 参数, 该参数建议只有在测试时开启")
	}

	if len(p.args.String("k")) > 32 {
		pkg.Error("密钥必须小于等于32位!")
		os.Exit(1)
	}
	secretKey := p.args.String("k")
	for len(secretKey)%16 != 0 {
		secretKey += "0"
	}
	_ = p.args.Set("k", secretKey)

	if p.args.Bool("c") {
		go p.randomRequestHttp()

		if err := p.runClient(); err != nil {
			pkg.Fatal("run client failed %s", err)
		}

		select {}
	}

	if !p.args.Bool("c") {
		go func() {
			for range time.Tick(time.Second * 60) {
				server.Show(time.Duration(p.args.Int64("o")) * time.Second)
			}
		}()
		if p.args.String("a") != "" {
			go p.startHttpServer()
		}

		if err := p.runServer(); err != nil {
			pkg.Fatal("run server failed %s", err)
		}
	}

}

func (p *proxyService) runClient() error {
	id, _ := machineid.ID()
	pools := strings.Split(p.args.String("u"), ",")
	for index, port := range strings.Split(p.args.String("l"), ",") {
		port = strings.ReplaceAll(port, " ", "")
		if port == "" {
			continue
		}
		if len(pools) < index {
			return errors.Errorf("-l参数: %s, --pool参数:%s; 必须一一对应", p.args.String("l"), p.args.String("u"))
		}
		pools[index] = strings.ReplaceAll(pools[index], " ", "")
		clientId := pkg.Crc32IEEEStr(fmt.Sprintf("%s-%s-%s-%s-%s", id,
			p.args.String("k"), p.args.String("r"), port, pools[index]))

		if err := pkg.Try(func() bool {
			if err := client.InitServerManage(p.args.Int("n"), p.args.String("k"), p.args.String("r"), clientId, pools[index]); err != nil {
				pkg.Error("连接到 %s 失败, 请检查到服务端的防火墙是否开放该端口, 或者检查服务端是否启动! 错误信息: %s", p.args.String("r"), err)
				time.Sleep(time.Second)
				return false
			}
			return true
		}, 1000); err != nil {
			pkg.Fatal("连接到服务器失败!")
		}

		fmt.Printf("监听端口 '%s', 矿池地址: '%s'\n", port, pools[index])
		go func(pool, clientId, port string) {
			if err := client.RunClient(port, p.args.String("k"), p.args.String("r"), pool, clientId); err != nil {
				pkg.Panic("初始化%s客户端失败: %s", clientId, err)
			}
		}(pools[index], clientId, port)
	}
	return nil
}

func (p *proxyService) runServer() error {
	return server.NewServer(p.args.String("l"), p.args.String("k"), p.args.String("r"))
}

func (p *proxyService) Stop(_ service.Service) error {
	return nil
}

func getArgs() []string {
	var result []string
	cmds := []string{
		"install", "remove", "stop", "restart", "start", "stat", "--delete",
	}
A:
	for _, v := range os.Args[1:] {
		for _, c := range cmds {
			if strings.Contains(v, c) {
				continue A
			}
		}
		result = append(result, v)
	}
	return result
}

func Install(c *cli.Context) error {
	s, err := NewService(c)
	if err != nil {
		return err
	}
	status, _ := s.Status()
	switch status {
	case service.StatusStopped, service.StatusRunning:
		if !c.Bool("delete") {
			pkg.Warn("已经存在一个服务!如果你需要重新安装请在本次参数尾部加上 --delete")
			return nil
		}
		if status == service.StatusRunning {
			_ = s.Stop()
		}
		if err := s.Uninstall(); err != nil {
			return errors.Wrap(err, "卸载存在的服务失败")
		}
		pkg.Info("成功卸载已经存在的服务")
	}
	if err := s.Install(); err != nil {
		return errors.Wrap(err, "安装服务失败")
	}
	return Start(c)
}

func Remove(c *cli.Context) error {
	s, err := NewService(c)
	if err != nil {
		return err
	}
	status, _ := s.Status()
	switch status {
	case service.StatusStopped, service.StatusRunning, service.StatusUnknown:
		if status == service.StatusRunning {
			_ = s.Stop()
		}
		if err := s.Uninstall(); err != nil {
			return errors.Wrap(err, "卸载服务失败")
		}
		pkg.Info("成功卸载服务")
	}
	return nil
}

func Restart(c *cli.Context) error {
	s, err := NewService(c)
	if err != nil {
		return err
	}
	status, _ := s.Status()
	switch status {
	case service.StatusStopped, service.StatusRunning, service.StatusUnknown:
		if err := s.Restart(); err != nil {
			return errors.Wrap(err, "重新启动服务失败")
		}
		status, _ := s.Status()
		if status != service.StatusRunning {
			return errors.New("该服务没有正常启动, 请查看日志!")
		}
		pkg.Info("重新启动服务成功")
	}
	return nil
}

func Start(c *cli.Context) error {
	s, err := NewService(c)
	if err != nil {
		return err
	}
	status, _ := s.Status()
	switch status {
	case service.StatusRunning:
		pkg.Info("服务已经在运行了")
		return nil
	case service.StatusStopped, service.StatusUnknown:
		if err := s.Start(); err != nil {
			return errors.Wrap(err, "启动服务失败")
		}
		pkg.Info("启动服务成功")
		return nil
	}
	return errors.New("服务还没有使用install安装!")
}

func Stop(c *cli.Context) error {
	s, err := NewService(c)
	if err != nil {
		return err
	}
	status, _ := s.Status()
	switch status {
	case service.StatusRunning:
		if err := s.Stop(); err != nil {
			return errors.Wrap(err, "停止服务失败")
		}
		return nil
	}
	pkg.Info("停止服务成功")
	return nil
}

func NewService(c *cli.Context) (service.Service, error) {
	svcConfig := &service.Config{
		Name:        "miner-proxy",
		DisplayName: "miner-proxy",
		Description: "miner encryption proxy service",
		Arguments:   getArgs(),
	}
	return service.New(&proxyService{args: c}, svcConfig)
}

var (
	Usages = []string{
		"以服务的方式安装客户端: ./miner-proxy install -c -d -l :9999 -r 服务端ip:服务端端口 -k 密钥 -u 客户端指定的矿池域名:矿池端口",
		"\t 以服务的方式安装服务端: ./miner-proxy install  -d -l :9998 -r 默认矿池域名:默认矿池端口 -k 密钥",
		"\t 更新以服务的方式安装的客户端/服务端: ./miner-proxy restart",
		"\t 在客户端/服务端添加微信掉线通知的订阅用户: ./miner-proxy add_wx_user -w appToken",
		"\t 服务端增加掉线通知: ./miner-proxy install -d -l :9998 -r 默认矿池域名:默认矿池端口 -k 密钥 --w appToken",
		"\t linux查看以服务的方式安装的日志: journalctl -f -u miner-proxy",
		"\t 客户端监听多个端口并且每个端口转发不同的矿池: ./miner-proxy -l :监听端口1,:监听端口2,:监听端口3 -r 服务端ip:服务端端口 -u 矿池链接1,矿池链接2,矿池链接3 -k 密钥 -d",
	}
)

func main() {
	flags := []cli.Flag{
		cli.BoolFlag{
			Name:  "c",
			Usage: "标记当前运行的是客户端",
		},
		cli.BoolFlag{
			Name:  "d",
			Usage: "是否开启debug, 如果开启了debug参数将会打印更多的日志",
		},
		cli.StringFlag{
			Name:  "l",
			Usage: "当前程序监听的地址",
			Value: ":9999",
		},
		cli.StringFlag{
			Name:  "r",
			Usage: "远程矿池地址或者远程本程序的监听地址 (default \"localhost:80\")",
			Value: "127.0.0.1:80",
		},
		cli.StringFlag{
			Name:  "f",
			Usage: "将日志写入到指定的文件中",
		},
		cli.StringFlag{
			Name:  "k",
			Usage: "数据包加密密钥, 长度小于等于32位",
		},
		cli.StringFlag{
			Name:  "a",
			Usage: "网页查看状态端口",
		},
		cli.StringFlag{
			Name:  "u",
			Usage: "客户端如果设置了这个参数, 那么服务端将会直接使用客户端的参数连接, 如果需要多个矿池, 请使用 -l :端口1,端口2,端口3 -P 矿池1,矿池2,矿池3",
		},
		cli.StringFlag{
			Name:  "w",
			Usage: "掉线微信通知token, 该参数只有在服务端生效, ,请在 https://wxpusher.zjiecode.com/admin/main/app/appToken 注册获取appToken",
		},
		cli.IntFlag{
			Name:  "o",
			Usage: "掉线多少秒之后就发送微信通知,默认4分钟",
			Value: 360,
		},
		cli.StringFlag{
			Name:  "p",
			Usage: "访问网页端时的密码, 如果没有设置, 那么网页端将不需要密码即可查看!固定的用户名为:admin",
		},
		cli.StringFlag{
			Name:  "g",
			Usage: "服务端参数, 使用指定的网址加速github下载, 示例: -g https://gh.api.99988866.xyz/  将会使用 https://gh.api.99988866.xyz/https://github.com/PerrorOne/miner-proxy/releases/download/{tag}/miner-proxy下载",
		},
		cli.IntFlag{
			Name:  "n",
			Value: 10,
			Usage: "客户端参数, 指定客户端启动时对于每一个转发端口通过多少tcp隧道连接服务端, 如果不清楚请保持默认, 不要设置小于2",
		},
	}

	app := &cli.App{
		Name:      "miner-proxy",
		UsageText: strings.Join(Usages, "\n"),
		Commands: []cli.Command{
			{
				Name:   "install",
				Usage:  "./miner-proxy install: 将代理安装到系统服务中, 开机自启动, 必须使用root或者管理员权限运行",
				Action: Install,
				Flags: []cli.Flag{
					cli.BoolFlag{
						Name:  "delete",
						Usage: "如果已经存在一个服务, 那么直接删除后,再安装",
					},
				},
			},
			{
				Name:   "remove",
				Usage:  "./miner-proxy remove: 将代理从系统服务中移除",
				Action: Remove,
			},
			{
				Name:   "restart",
				Usage:  "./miner-proxy restart: 重新启动已经安装到系统服务的代理",
				Action: Restart,
			},
			{
				Name:   "start",
				Usage:  "./miner-proxy start: 启动已经安装到系统服务的代理",
				Action: Start,
			},
			{
				Name:   "stop",
				Usage:  "./miner-proxy start: 停止已经安装到系统服务的代理",
				Action: Stop,
			},
			{
				Name:  "add_wx_user",
				Usage: "./miner-proxy add_wx_user: 添加微信用户到掉线通知中",
				Flags: []cli.Flag{
					cli.StringFlag{
						Name:     "w",
						Required: true,
						Usage:    "掉线微信通知token, 该参数只有在服务端生效, ,请在 https://wxpusher.zjiecode.com/admin/main/app/appToken 注册获取appToken",
					},
				},
				Action: func(c *cli.Context) error {
					return (&proxyService{args: c}).checkWxPusher(c.String("w"), true)
				},
			},
		},
		Flags: flags,
		Action: func(c *cli.Context) error {
			var logLevel = zapcore.InfoLevel
			if c.Bool("d") {
				logLevel = zapcore.DebugLevel
			}
			pkg.InitLog(logLevel, c.String("f"))
			if c.String("w") != "" {
				if err := (&proxyService{args: c}).checkWxPusher(c.String("w"), false); err != nil {
					pkg.Fatal(err.Error())
				}
			}

			s, err := NewService(c)
			if err != nil {
				return err
			}
			return s.Run()
		},
	}

	pkg.PrintHelp()
	fmt.Printf("版本:%s\n更新日志:%s\n", version, gitCommit)
	if err := app.Run(os.Args); err != nil {
		pkg.Fatal("启动代理失败: %s", err)
	}
}
