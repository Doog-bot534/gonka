#!/bin/bash
if [ -n "$SSH_KEY_PATH" ]; then
  SSH_KEY_ARG="-i $SSH_KEY_PATH"
else
  SSH_KEY_ARG=""
fi

scp $SSH_KEY_ARG -P 18132 launch.py decentai@xj7-5.s.filfox.io:/srv/dai/
scp $SSH_KEY_ARG -P 18132 join-additional/18132.sh decentai@xj7-5.s.filfox.io:/srv/dai/join.sh
scp $SSH_KEY_ARG -P 18133 launch.py decentai@xj7-5.s.filfox.io:/srv/dai/
scp $SSH_KEY_ARG -P 18133 join-additional/18133.sh decentai@xj7-5.s.filfox.io:/srv/dai/join.sh
scp $SSH_KEY_ARG -P 18134 launch.py decentai@xj7-5.s.filfox.io:/srv/dai/
scp $SSH_KEY_ARG -P 18134 join-additional/18134.sh decentai@xj7-5.s.filfox.io:/srv/dai/join.sh
