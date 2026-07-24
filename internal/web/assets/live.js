(function () {
	var banner = document.getElementById("fresh");
	var marker = document.body.getAttribute("data-latest");
	if (!banner || marker === null || typeof EventSource === "undefined") {
		return;
	}

	var stream = new EventSource("/events?since=" + encodeURIComponent(marker));
	stream.addEventListener("fresh", function () {
		banner.hidden = false;
	});

	document.addEventListener("visibilitychange", function () {
		if (document.visibilityState === "hidden") {
			stream.close();
		} else if (stream.readyState === 2 && banner.hidden) {
			stream = new EventSource("/events?since=" + encodeURIComponent(marker));
			stream.addEventListener("fresh", function () {
				banner.hidden = false;
			});
		}
	});
})();
