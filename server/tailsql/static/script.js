(() => {
    const query = document.getElementById('query');
    const qButton = document.getElementById('send-query');
    const dlButton = document.getElementById("dl-button");
    const qform = document.getElementById('qform');
    const output = document.getElementById('output');
    const origin = document.location.origin;
    const sources = document.getElementById('sources');
    const body = document.getElementById('tsql');

    function hasQuery() {
        return query.value.trim() != "";
    }

    query.addEventListener("keydown", (evt) => {
        if (evt.shiftKey && evt.key == "Enter") {
            evt.preventDefault();
            if (hasQuery()) {
                qButton.click();
            }
        }
    })

    body.addEventListener("keyup", (evt) => {
        if (evt.altKey) {
            var c = evt.code.match(/^Digit(\d)$/);
            if (c) {
                var v = parseInt(c[1]);
                if (v > 0 && v <= sources.options.length) {
                    evt.preventDefault();
                    sources.options[v-1].selected = true;
                }
            }
        }
    });

    function performDownload(name, url) {
        var link = document.createElement('a');
        link.setAttribute('href', url);
        link.setAttribute('download', name);
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
    }

    function disableIfNoOutput(elt) {
        return (evt) => {
            elt.disabled = !output;
        }
    }

    dlButton.addEventListener("click", (evt) => {
        var fd = new FormData(qform);
        var sp = new URLSearchParams(fd);

        var href = origin + "/csv?" + sp.toString();
        performDownload('query.csv', href);
    });

    // Disable the download button when there are no query results.
    window.addEventListener("load", disableIfNoOutput(dlButton));
    // Initially focus the query input.
    window.addEventListener("load", (evt) => { query.focus(); });
    // Refresh when the input source changes.
    sources.addEventListener('change', (evt) => { qform.submit() });
})()
