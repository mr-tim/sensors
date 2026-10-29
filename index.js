import eWeLink from 'ewelink-api-next'
import http from 'node:http'
import { exec } from 'node:child_process'
import fs from 'node:fs/promises'

const REDIRECT_URL = 'http://localhost:8080';
const TOKEN_FILE = 'token.json';

const waitForOauthRedirect = () => new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
        const url = new URL(req.url, REDIRECT_URL);
        const code = url.searchParams.get('code');
        const region = url.searchParams.get('region');

        if (!code) {
            res.writeHead(404);
            res.end();
            return;
        }

        res.writeHead(200, {'Content-Type': 'text/html'});
        res.end('<html><body>Login successful, you can close this tab.</body></html>');
        server.close();
        resolve({code, region});
    });
    server.on('error', reject);
    server.listen(8080);
});

const loadSavedToken = async () => {
    try {
        const saved = JSON.parse(await fs.readFile(TOKEN_FILE, 'utf8'));
        if (saved.accessToken && saved.atExpiredTime > Date.now()) {
            return saved;
        }
    } catch {
        // no saved token yet, or unreadable — fall through to the oauth flow
    }
    return null;
};

const oauthLogin = async (client) => {
    const loginUrl = await client.oauth.createLoginUrl({
        redirectUrl: REDIRECT_URL,
    });
    console.log({loginUrl});

    const redirectPromise = waitForOauthRedirect();
    exec(`open "${loginUrl}"`);

    const {code, region} = await redirectPromise;
    console.log({code, region});

    const token = await client.oauth.getToken({code, region, redirectUrl: REDIRECT_URL});
    console.log({token});

    const tokenData = {...token.data, region};
    await fs.writeFile(TOKEN_FILE, JSON.stringify(tokenData, null, 2));
    return tokenData;
};

(async () => {


    const clientOptions = {
        appId: 'unlajQcOZwmATNgDWNLJn5Dq0b1XmRBi',
        appSecret: 'VhCgHlX0zCHvNbY352i0dxICM9t7zdmu',
        region: 'eu',
    };
    const client = new eWeLink.WebAPI(clientOptions);

    let tokenData = await loadSavedToken();
    if (tokenData) {
        console.log('Using saved token from', TOKEN_FILE);
    } else {
        tokenData = await oauthLogin(client);
    }

    client.at = tokenData.accessToken;

    // const allThings = await client.device.getAllThings();
    // const things = allThings.data.thingList;
    // console.log(JSON.stringify(things, null, 2));

    // a48013db7e -- SNZB-02LD - temp probe
    // a48013db7a -- SNZB-02P - temp/humidity dot
    //const tempProbeID = 'a48013db7e';
    // const response = await client.device.getThingStatus({
    //     id: tempProbeID,
    //     type: 'device'
    // })


    const ws = new eWeLink.Ws(clientOptions);
    ws.at = tokenData.accessToken;

    const getUserApiKey = async ()=> {
        const response = await client.home.getFamily({});
        return response.error === 0 ? response.data.familyList[0]?.apikey : null;
    }

    ws.userApiKey = await getUserApiKey();

    console.log('user api key:', ws.userApiKey)

    const p = new Promise((res, rej) => {});

    const wsConnOptions = {
        appId: clientOptions.appId,
        at: ws.at,
        region: ws.region,
        userApiKey: ws.userApiKey
    };

    console.log('wsConnOptions', JSON.stringify(wsConnOptions, null, 2));

    const wsConn = await ws.Connect.create(wsConnOptions,
        () => {console.log('ws open')},
        () => {console.log('ws closed')},
        (err) => {console.error('ws error:', err)},
        (ws, msg) => { console.log('ws msg:', msg.data)}
    );

    await p;

    // const response = client.device.getOperationHistory({
    //     deviceId: tempProbeID,
    // })
    //
    //
    // console.log(JSON.stringify(response, null, 2));
})();
