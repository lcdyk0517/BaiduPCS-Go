package pcscommand

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	"github.com/mdp/qrterminal/v3"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"github.com/qjfoidnh/BaiduPCS-Go/requester"
)

const (
	qrCodeGetURL     = "https://passport.baidu.com/v2/api/getqrcode?lp=pc"
	qrCodeChannelURL = "https://passport.baidu.com/channel/unicast?channel_id=%s&callback="
	qrCodeLoginURL   = "https://passport.baidu.com/v3/login/main/qrbdusslogin?bduss=%s"

	defaultQRCodeTimeout = 5 * time.Minute
	qrPollInterval       = 2 * time.Second
)

type QRCodeStatus int

const (
	QRStatusWaiting QRCodeStatus = iota
	QRStatusScanned
	QRStatusConfirmed
	QRStatusExpired
	QRStatusError
)

type qrCodeResponse struct {
	ImgURL string `json:"imgurl"`
	Sign   string `json:"sign"`
	Errno  int    `json:"errno"`
}

type qrChannelResponse struct {
	Errno    int    `json:"errno"`
	ChannelV string `json:"channel_v"`
}

type qrChannelData struct {
	Status int    `json:"status"`
	V      string `json:"v"`
}

type QRLoginResult struct {
	Status    QRCodeStatus
	TempBDUSS string
	Message   string
}

type QRLoginClient struct {
	httpClient *http.Client
	userAgent  string
}

func NewQRLoginClient() *QRLoginClient {
	jar, _ := cookiejar.New(nil)
	return &QRLoginClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
		userAgent: requester.UserAgent,
	}
}

func (c *QRLoginClient) GetQRCode() (sign, imgURL string, err error) {
	req, err := http.NewRequest(http.MethodGet, qrCodeGetURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求二维码失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应失败: %v", err)
	}

	var qrResp qrCodeResponse
	if err := json.Unmarshal(body, &qrResp); err != nil {
		return "", "", fmt.Errorf("解析响应失败: %v", err)
	}
	if qrResp.Errno != 0 {
		return "", "", fmt.Errorf("获取二维码失败: errno=%d", qrResp.Errno)
	}

	imgURL = qrResp.ImgURL
	if !strings.HasPrefix(imgURL, "http") {
		imgURL = "https://" + imgURL
	}
	return qrResp.Sign, imgURL, nil
}

func (c *QRLoginClient) SaveQRCodeImage(imgURL, path string) error {
	req, err := http.NewRequest(http.MethodGet, imgURL, nil)
	if err != nil {
		return fmt.Errorf("创建二维码图片请求失败: %v", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("下载二维码图片失败: %v", err)
	}
	defer resp.Body.Close()

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建二维码图片文件失败: %v", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("保存二维码图片失败: %v", err)
	}
	return nil
}

func (c *QRLoginClient) CheckQRStatus(sign string) (*QRLoginResult, error) {
	url := fmt.Sprintf(qrCodeChannelURL, sign)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求状态失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	respStr := strings.TrimPrefix(string(body), "(")
	respStr = strings.TrimSuffix(respStr, ")")

	var channelResp qrChannelResponse
	if err := json.Unmarshal([]byte(respStr), &channelResp); err != nil {
		return &QRLoginResult{Status: QRStatusWaiting, Message: "等待扫码"}, nil
	}
	if channelResp.Errno != 0 || channelResp.ChannelV == "" {
		return &QRLoginResult{Status: QRStatusWaiting, Message: "等待扫码"}, nil
	}

	var channelData qrChannelData
	channelV := strings.ReplaceAll(channelResp.ChannelV, "\\", "")
	if err := json.Unmarshal([]byte(channelV), &channelData); err != nil {
		if err := json.Unmarshal([]byte(channelResp.ChannelV), &channelData); err != nil {
			return &QRLoginResult{
				Status:  QRStatusError,
				Message: fmt.Sprintf("解析状态数据失败: %v", err),
			}, nil
		}
	}

	if channelData.Status == 0 && channelData.V != "" {
		return &QRLoginResult{
			Status:    QRStatusConfirmed,
			TempBDUSS: channelData.V,
			Message:   "扫码确认成功",
		}, nil
	}

	return &QRLoginResult{
		Status:  QRStatusScanned,
		Message: "已扫码，请在手机上确认登录",
	}, nil
}

func (c *QRLoginClient) ExchangeBDUSS(tempBDUSS string) (bduss, stoken, cookies string, err error) {
	url := fmt.Sprintf(qrCodeLoginURL, tempBDUSS)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	c.httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() {
		c.httpClient.CheckRedirect = nil
	}()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("请求登录失败: %v", err)
	}
	defer resp.Body.Close()

	var cookieParts []string
	for _, cookie := range resp.Header.Values("Set-Cookie") {
		parts := strings.Split(cookie, ";")
		if len(parts) > 0 {
			cookieParts = append(cookieParts, parts[0])
		}
		if strings.Contains(cookie, "BDUSS=") {
			matches := regexp.MustCompile(`BDUSS=([^;]+)`).FindStringSubmatch(cookie)
			if len(matches) >= 2 {
				bduss = matches[1]
			}
		}
		if strings.Contains(cookie, "STOKEN=") {
			matches := regexp.MustCompile(`STOKEN=([^;]+)`).FindStringSubmatch(cookie)
			if len(matches) >= 2 {
				stoken = matches[1]
			}
		}
	}
	cookies = strings.Join(cookieParts, "; ")

	if bduss == "" {
		return "", "", "", fmt.Errorf("未能从响应中获取BDUSS")
	}
	return bduss, stoken, cookies, nil
}

func decodeQRCodeFromURL(imgURL string) (string, error) {
	resp, err := http.Get(imgURL)
	if err != nil {
		return "", fmt.Errorf("下载二维码图片失败: %v", err)
	}
	defer resp.Body.Close()

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return "", fmt.Errorf("解码二维码图片失败: %v", err)
	}

	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", fmt.Errorf("转换二维码图片失败: %v", err)
	}

	result, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return "", fmt.Errorf("解析二维码失败: %v", err)
	}
	return result.GetText(), nil
}

func printQRCodeToTerminal(imgURL string) {
	fmt.Println()
	fmt.Println("请使用百度网盘APP扫描以下二维码登录：")
	fmt.Println()

	qrContent, err := decodeQRCodeFromURL(imgURL)
	if err != nil {
		fmt.Printf("无法生成终端二维码: %v\n", err)
		fmt.Printf("请打开以下链接查看并扫描二维码：\n%s\n\n", imgURL)
		return
	}

	qrterminal.GenerateHalfBlock(qrContent, qrterminal.L, os.Stdout)
	fmt.Println()
	fmt.Printf("或打开以下链接查看二维码：\n%s\n\n", imgURL)
}

func RunQRLogin(timeout time.Duration, qrFile string) error {
	if timeout <= 0 {
		timeout = defaultQRCodeTimeout
	}

	client := NewQRLoginClient()

	fmt.Println("正在获取登录二维码...")
	sign, imgURL, err := client.GetQRCode()
	if err != nil {
		return fmt.Errorf("获取二维码失败: %v", err)
	}

	if qrFile != "" {
		if err := client.SaveQRCodeImage(imgURL, qrFile); err != nil {
			return err
		}
		fmt.Printf("二维码图片已保存到: %s\n", qrFile)
	}

	printQRCodeToTerminal(imgURL)
	fmt.Println("等待扫码...")

	startTime := time.Now()
	ticker := time.NewTicker(qrPollInterval)
	defer ticker.Stop()

	lastStatus := QRStatusWaiting
	for range ticker.C {
		if time.Since(startTime) > timeout {
			return fmt.Errorf("二维码已过期，请重新运行 qrlogin 命令")
		}

		result, err := client.CheckQRStatus(sign)
		if err != nil {
			continue
		}

		if result.Status != lastStatus {
			lastStatus = result.Status
			switch result.Status {
			case QRStatusScanned:
				fmt.Println("已扫码，请在手机上确认登录...")
			case QRStatusConfirmed:
				fmt.Println("扫码确认成功，正在完成登录...")
			}
		}

		if result.Status == QRStatusConfirmed && result.TempBDUSS != "" {
			bduss, stoken, cookies, err := client.ExchangeBDUSS(result.TempBDUSS)
			if err != nil {
				return fmt.Errorf("交换凭证失败: %v", err)
			}

			baidu, err := pcsconfig.Config.SetupUserByBDUSS(bduss, "", stoken, cookies)
			if err != nil {
				return fmt.Errorf("保存用户配置失败: %v", err)
			}

			fmt.Printf("\n登录成功！用户名: %s\n", baidu.Name)
			return nil
		}
	}

	return nil
}

func RunQRLoginInit(qrFile string) error {
	client := NewQRLoginClient()
	sign, imgURL, err := client.GetQRCode()
	if err != nil {
		return fmt.Errorf("获取二维码失败: %v", err)
	}

	if qrFile != "" {
		if err := client.SaveQRCodeImage(imgURL, qrFile); err != nil {
			return err
		}
		fmt.Printf("qr_file=%s\n", qrFile)
	}
	fmt.Printf("sign=%s\n", sign)
	fmt.Printf("img_url=%s\n", imgURL)
	return nil
}

func RunQRLoginStatus(sign string) error {
	client := NewQRLoginClient()
	result, err := client.CheckQRStatus(sign)
	if err != nil {
		return err
	}

	switch result.Status {
	case QRStatusWaiting:
		fmt.Println("status=waiting")
	case QRStatusScanned:
		fmt.Println("status=scanned")
	case QRStatusConfirmed:
		fmt.Println("status=confirmed")
		fmt.Printf("temp_bduss=%s\n", result.TempBDUSS)
	case QRStatusExpired:
		fmt.Println("status=expired")
	default:
		fmt.Println("status=error")
	}
	if result.Message != "" {
		fmt.Printf("message=%s\n", result.Message)
	}
	return nil
}

func RunQRLoginFinish(tempBDUSS string) error {
	client := NewQRLoginClient()
	bduss, stoken, cookies, err := client.ExchangeBDUSS(tempBDUSS)
	if err != nil {
		return fmt.Errorf("交换凭证失败: %v", err)
	}

	baidu, err := pcsconfig.Config.SetupUserByBDUSS(bduss, "", stoken, cookies)
	if err != nil {
		return fmt.Errorf("保存用户配置失败: %v", err)
	}

	fmt.Printf("uid=%d\n", baidu.UID)
	fmt.Printf("name=%s\n", baidu.Name)
	return nil
}

func GenerateASCIIQRCodeForURL(imgURL string) (string, error) {
	qrContent, err := decodeQRCodeFromURL(imgURL)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	config := qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    &buf,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	}
	qrterminal.GenerateWithConfig(qrContent, config)
	return buf.String(), nil
}
