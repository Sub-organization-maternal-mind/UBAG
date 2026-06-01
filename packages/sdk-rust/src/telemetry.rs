//! W3C trace-context helpers.

pub fn build_traceparent(trace_id: &str, span_id: &str) -> String {
    format!("00-{}-{}-01", trace_id, span_id)
}

/// Returns (trace_id, span_id) if the traceparent is well-formed.
pub fn parse_traceparent(value: &str) -> Option<(String, String)> {
    let parts: Vec<&str> = value.split('-').collect();
    if parts.len() != 4 || parts[0] != "00" || parts[1].len() != 32 || parts[2].len() != 16 {
        return None;
    }
    if !parts[1].chars().all(|c| c.is_ascii_hexdigit()) || !parts[2].chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    Some((parts[1].to_string(), parts[2].to_string()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip() {
        let tp = build_traceparent("0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331");
        let (tid, sid) = parse_traceparent(&tp).unwrap();
        assert_eq!(tid, "0af7651916cd43dd8448eb211c80319c");
        assert_eq!(sid, "b7ad6b7169203331");
    }

    #[test]
    fn rejects_garbage() {
        assert!(parse_traceparent("garbage").is_none());
    }
}
