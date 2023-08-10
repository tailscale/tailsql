(() => {
    const query = document.getElementById('query');
    const dlButton = document.getElementById("dl-button");
    const qform = document.getElementById("qform");
    const output = document.getElementById('output');
    const origin = document.location.origin;
    const sources = document.getElementById('sources');

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
