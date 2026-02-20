BINARY = bin/go_job
SERVICE = go-search-jobs

.PHONY: build deploy restart clean

build:
	go build -o $(BINARY) .

deploy: build
	cp deploy/go_job.service $(HOME)/.config/systemd/user/$(SERVICE).service
	systemctl --user daemon-reload
	systemctl --user restart $(SERVICE)
	@echo "Deployed and restarted $(SERVICE)"

restart:
	systemctl --user restart $(SERVICE)

clean:
	rm -f $(BINARY)
