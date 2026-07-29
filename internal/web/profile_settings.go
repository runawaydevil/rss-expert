package web

import (
	"context"
	"io"
	"net/http"

	"github.com/runawaydevil/rss-expert/internal/identity"
	"github.com/runawaydevil/rss-expert/internal/media"
)

func (a *App) profileSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	profile, err := a.accounts.Profile(ctx, account.ID)
	if err != nil {
		a.log.Error("could not read profile", "error", err)
	}

	handle, err := a.posts.EnsureHandle(ctx, account)
	if err != nil {
		a.log.Error("could not resolve a handle", "error", err)
	}

	a.render(w, r, "editprofile.html", map[string]any{
		"Title":     "Your profile — RSS Expert",
		"Handle":    handle,
		"Host":      a.host(),
		"Profile":   profile,
		"AvatarURL": mediaURL(profile.AvatarSHA),
		"BannerURL": mediaURL(profile.BannerSHA),
		"MaxBio":    identity.MaxBio,
		"Problem":   r.URL.Query().Get("problem"),
	})
}

func (a *App) saveProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := accountFrom(ctx)

	if err := r.ParseMultipartForm(media.MaxUploadBytes); err != nil {
		a.profileProblem(w, r, "that form could not be read")
		return
	}
	if !validCSRF(r) {
		a.profileProblem(w, r, "that form expired")
		return
	}

	current, err := a.accounts.Profile(ctx, account.ID)
	if err != nil {
		a.log.Error("could not read profile", "error", err)
	}

	next := identity.Profile{
		DisplayName: r.PostFormValue("display_name"),
		Bio:         r.PostFormValue("bio"),
		AvatarSHA:   current.AvatarSHA,
		BannerSHA:   current.BannerSHA,
	}

	if r.PostFormValue("remove_avatar") != "" {
		next.AvatarSHA = ""
	}
	if r.PostFormValue("remove_banner") != "" {
		next.BannerSHA = ""
	}

	if sha, problem := a.uploadImage(ctx, account.ID, r, "avatar"); problem != "" {
		a.profileProblem(w, r, problem)
		return
	} else if sha != "" {
		next.AvatarSHA = sha
	}
	if sha, problem := a.uploadImage(ctx, account.ID, r, "banner"); problem != "" {
		a.profileProblem(w, r, problem)
		return
	} else if sha != "" {
		next.BannerSHA = sha
	}

	if err := a.accounts.SetProfile(ctx, account.ID, next); err != nil {
		a.log.Error("could not save profile", "error", err)
		a.profileProblem(w, r, "that profile could not be saved")
		return
	}

	a.moderation.Log(ctx, account, "profile.edit", "", "")
	if handle, err := a.posts.HandleFor(ctx, account.ID); err == nil && handle != "" {
		a.AnnounceProfile(context.WithoutCancel(ctx), account.ID, handle)
	}
	http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
}

func (a *App) uploadImage(ctx context.Context, accountID int64, r *http.Request, field string) (string, string) {
	file, header, err := r.FormFile(field)
	if err != nil {
		return "", ""
	}
	defer file.Close()

	body, err := io.ReadAll(io.LimitReader(file, media.MaxUploadBytes+1))
	if err != nil {
		return "", "that image could not be read"
	}

	name := ""
	if header != nil {
		name = header.Filename
	}

	stored, err := a.media.Put(ctx, accountID, name, body, "")
	if err != nil {
		return "", err.Error()
	}
	if !stored.IsImage() {
		return "", "a profile picture has to be an image"
	}
	return stored.SHA256, ""
}

func (a *App) profileProblem(w http.ResponseWriter, r *http.Request, problem string) {
	http.Redirect(w, r, "/settings/profile?problem="+urlEscape(problem), http.StatusSeeOther)
}

func mediaURL(sha string) string {
	if sha == "" {
		return ""
	}
	return "/media/" + sha
}
