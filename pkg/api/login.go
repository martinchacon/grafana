package api

import (
	"net/url"

	"github.com/grafana/grafana/pkg/api/dtos"
	"github.com/grafana/grafana/pkg/api/keystone"
	"github.com/grafana/grafana/pkg/bus"
	"github.com/grafana/grafana/pkg/log"
	"github.com/grafana/grafana/pkg/login"
	"github.com/grafana/grafana/pkg/metrics"
	"github.com/grafana/grafana/pkg/middleware"
	m "github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/util"
)

const (
	VIEW_INDEX = "index"
)

func LoginView(c *middleware.Context) {
	viewData, err := setIndexViewData(c)
	if err != nil {
		c.Handle(500, "Failed to get settings", err)
		return
	}

	enabledOAuths := make(map[string]interface{})
	for key, oauth := range setting.OAuthService.OAuthInfos {
		enabledOAuths[key] = map[string]string{"name": oauth.Name}
	}

	viewData.Settings["oauth"] = enabledOAuths
	viewData.Settings["disableUserSignUp"] = !setting.AllowUserSignUp
	viewData.Settings["loginHint"] = setting.LoginHint
	viewData.Settings["disableLoginForm"] = setting.DisableLoginForm

	if !tryLoginUsingRememberCookie(c) {
		c.HTML(200, VIEW_INDEX, viewData)
		return
	}

	if redirectTo, _ := url.QueryUnescape(c.GetCookie("redirect_to")); len(redirectTo) > 0 {
		c.SetCookie("redirect_to", "", -1, setting.AppSubUrl+"/")
		c.Redirect(redirectTo)
		return
	}

	c.Redirect(setting.AppSubUrl + "/")
}

func tryLoginUsingRememberCookie(c *middleware.Context) bool {
	// Check auto-login.
	uname := c.GetCookie(setting.CookieUserName)
	if len(uname) == 0 {
		return false
	}

	isSucceed := false
	defer func() {
		if !isSucceed {
			log.Trace("auto-login cookie cleared: %s", uname)
			c.SetCookie(setting.CookieUserName, "", -1, setting.AppSubUrl+"/")
			c.SetCookie(setting.CookieRememberName, "", -1, setting.AppSubUrl+"/")
			return
		}
	}()

	userQuery := m.GetUserByLoginQuery{LoginOrEmail: uname}
	if err := bus.Dispatch(&userQuery); err != nil {
		return false
	}

	user := userQuery.Result

	// validate remember me cookie
	if val, _ := c.GetSuperSecureCookie(
		util.EncodeMd5(user.Rands+user.Password), setting.CookieRememberName); val != user.Login {
		return false
	}

	isSucceed = true
	loginUserWithUser(user, c)
	return true
}

func LoginApiPing(c *middleware.Context) {
	if !tryLoginUsingRememberCookie(c) {
		c.JsonApiErr(401, "Unauthorized", nil)
		return
	}

	c.JsonOK("Logged in")
}

func LoginPost(c *middleware.Context, cmd dtos.LoginCommand) Response {
	authQuery := login.LoginUserQuery{
		Username: cmd.User,
		Password: cmd.Password,
	}

	if err := bus.Dispatch(&authQuery); err != nil {
		if err == login.ErrInvalidCredentials {
			return ApiError(401, "Invalid username or password", err)
		}

		return ApiError(500, "Error while trying to authenticate user", err)
	}

	user := authQuery.User

	loginUserWithUser(user, c)

	if setting.KeystoneEnabled {
		if setting.KeystoneCredentialAesKey != "" {
			cmd.Password = keystone.EncryptPassword(cmd.Password)
		}
		if setting.KeystoneCookieCredentials {
			log.Debug("c.Req.Header.Get(\"X-Forwarded-Proto\"): %s", c.Req.Header.Get("X-Forwarded-Proto"))
			var days interface{}
			if setting.LogInRememberDays == 0 {
				days = nil
			} else {
				days = 86400 * setting.LogInRememberDays
			}
			c.SetCookie(middleware.SESS_KEY_PASSWORD, cmd.Password, days, setting.AppSubUrl+"/", nil, middleware.IsSecure(c), true)
		} else {
			c.Session.Set(middleware.SESS_KEY_PASSWORD, cmd.Password)
		}
	}

	result := map[string]interface{}{
		"message": "Logged in",
	}

	if redirectTo, _ := url.QueryUnescape(c.GetCookie("redirect_to")); len(redirectTo) > 0 {
		result["redirectUrl"] = redirectTo
		c.SetCookie("redirect_to", "", -1, setting.AppSubUrl+"/")
	}

	metrics.M_Api_Login_Post.Inc(1)

	return Json(200, result)
}

func loginUserWithUser(user *m.User, c *middleware.Context) {
	if user == nil {
		log.Error(3, "User login with nil user")
	}

	days := 86400 * setting.LogInRememberDays
	if days > 0 {
		c.SetCookie(setting.CookieUserName, user.Login, days, setting.AppSubUrl+"/", nil, middleware.IsSecure(c), true)
		c.SetSuperSecureCookie(util.EncodeMd5(user.Rands+user.Password),
			setting.CookieRememberName, user.Login, days, setting.AppSubUrl+"/", nil, middleware.IsSecure(c), true)
	}

	c.Session.Set(middleware.SESS_KEY_USERID, user.Id)
}

func Logout(c *middleware.Context) {
	c.SetCookie(setting.CookieUserName, "", -1, setting.AppSubUrl+"/", nil, middleware.IsSecure(c), true)
	c.SetCookie(setting.CookieRememberName, "", -1, setting.AppSubUrl+"/", nil, middleware.IsSecure(c), true)
	c.SetCookie(middleware.SESS_KEY_PASSWORD, "", -1, setting.AppSubUrl+"/", nil, middleware.IsSecure(c), true)
	c.Session.Destory(c)
	c.Redirect(setting.AppSubUrl + "/login")
}
