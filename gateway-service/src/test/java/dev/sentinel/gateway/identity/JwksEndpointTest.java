package dev.sentinel.gateway.identity;

import static org.hamcrest.Matchers.not;
import static org.hamcrest.Matchers.containsString;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.test.web.servlet.MockMvc;

@SpringBootTest
@AutoConfigureMockMvc
class JwksEndpointTest {

    @Autowired
    MockMvc mvc;

    @Test
    void publicKeysArePublishedWithoutAuthAndWithoutPrivateParts() throws Exception {
        mvc.perform(get("/.well-known/jwks.json"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.keys[0].kty").value("EC"))
                .andExpect(jsonPath("$.keys[0].crv").value("P-256"))
                .andExpect(jsonPath("$.keys[0].kid").exists())
                .andExpect(content().string(not(containsString("\"d\""))));
        mvc.perform(get("/.well-known/sentinel-issuer"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.jwks_uri").value(containsString("/.well-known/jwks.json")));
    }
}
